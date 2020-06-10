// Copyright 2020 The PipeCD Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package driffdetector provides a piped component
// that continuously checks configuration driff between the current live state
// and the state defined at the latest commit of all applications.
package driffdetector

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/kapetaniosci/pipe/pkg/app/api/service/pipedservice"
	"github.com/kapetaniosci/pipe/pkg/app/piped/livestatestore"
	"github.com/kapetaniosci/pipe/pkg/config"
	"github.com/kapetaniosci/pipe/pkg/git"
	"github.com/kapetaniosci/pipe/pkg/model"
)

type applicationLister interface {
	ListByCloudProvider(name string) []*model.Application
}

type gitClient interface {
	Clone(ctx context.Context, repoID, remote, branch, destination string) (git.Repo, error)
}

type apiClient interface {
	ReportApplicationSyncState(ctx context.Context, req *pipedservice.ReportApplicationSyncStateRequest, opts ...grpc.CallOption) (*pipedservice.ReportApplicationSyncStateResponse, error)
}

type Detector interface {
	Run(ctx context.Context) error
}

type detector struct {
	detectors []providerDetector
	logger    *zap.Logger
}

type providerDetector interface {
	Run(ctx context.Context) error
	ProviderName() string
}

func NewDetector(
	appLister applicationLister,
	gitClient gitClient,
	stateGetter livestatestore.Getter,
	apiClient apiClient,
	cfg *config.PipedSpec,
	logger *zap.Logger,
) *detector {

	r := &detector{
		detectors: make([]providerDetector, 0, len(cfg.CloudProviders)),
		logger:    logger.Named("driff-detector"),
	}

	for _, cp := range cfg.CloudProviders {
		switch cp.Type {
		case model.CloudProviderKubernetes:
			sg, ok := stateGetter.KubernetesGetter(cp.Name)
			if !ok {
				r.logger.Error(fmt.Sprintf("unabled to find live state getter for cloud provider: %s", cp.Name))
				continue
			}
			r.detectors = append(r.detectors, newKubernetesDetector(cp, appLister, gitClient, sg, apiClient, cfg, logger))

		default:
		}
	}

	return r
}

func (r *detector) Run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)

	for i, detector := range r.detectors {
		// Avoid starting all detectors at the same time to reduce the API call burst.
		time.Sleep(time.Duration(i) * 10 * time.Second)
		r.logger.Info(fmt.Sprintf("starting driff detector for cloud provider: %s", detector.ProviderName()))

		group.Go(func() error {
			return detector.Run(ctx)
		})
	}

	r.logger.Info(fmt.Sprintf("all driff detectors of %d providers have been started", len(r.detectors)))

	if err := group.Wait(); err != nil {
		r.logger.Error("failed while running", zap.Error(err))
		return err
	}

	r.logger.Info(fmt.Sprintf("all driff detectors of %d providers have been stopped", len(r.detectors)))
	return nil
}
