// Copyright 2018 Palantir Technologies, Inc.
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

package bulldozer

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v2"
)

type FetchedConfig struct {
	Owner  string
	Repo   string
	Ref    string
	Config *Config
	Error  error
}

func (fc FetchedConfig) Missing() bool {
	return fc.Config == nil && fc.Error == nil
}

func (fc FetchedConfig) Valid() bool {
	return fc.Config != nil && fc.Error == nil
}

func (fc FetchedConfig) Invalid() bool {
	return fc.Error != nil
}

func (fc FetchedConfig) String() string {
	return fmt.Sprintf("%s/%s ref=%s", fc.Owner, fc.Repo, fc.Ref)
}

type ConfigFetcher struct {
	configurationV1Path  string
	configurationV0Paths []string
}

func NewConfigFetcher(configurationV1Path string, configurationV0Paths []string) ConfigFetcher {
	return ConfigFetcher{
		configurationV1Path:  configurationV1Path,
		configurationV0Paths: configurationV0Paths,
	}
}

// ConfigForPR fetches the configuration for a PR. It returns an error
// only if the existence of the configuration file could not be determined. If the file
// does not exist or is invalid, the returned error is nil and the appropriate
// fields are set on the FetchedConfig.
func (cf *ConfigFetcher) ConfigForPR(ctx context.Context, client *github.Client, pr *github.PullRequest) (FetchedConfig, error) {
	fc := FetchedConfig{
		Owner: pr.GetBase().GetRepo().GetOwner().GetLogin(),
		Repo:  pr.GetBase().GetRepo().GetName(),
		Ref:   pr.GetBase().GetRef(),
	}

	logger := zerolog.Ctx(ctx)

	bytes, err := cf.fetchConfigContents(ctx, client, fc.Owner, fc.Repo, fc.Ref, cf.configurationV1Path)
	if err == nil && bytes != nil {
		config, err := cf.unmarshalConfig(bytes)
		if err != nil {
			logger.Debug().Msgf("v1 config is invalid")
		} else {
			fc.Config = config
			return fc, nil
		}
	}

	for _, configV0Path := range cf.configurationV0Paths {
		logger.Debug().Msgf("v1 configuration not found; will attempt fetch v0 %s and unmarshal as v0", configV0Path)
		bytes, err := cf.fetchConfigContents(ctx, client, fc.Owner, fc.Repo, fc.Ref, configV0Path)
		if err != nil {
			continue
		}

		if bytes == nil {
			continue
		}

		config, err := cf.unmarshalConfigV0(bytes)
		if err != nil {
			continue
		}
		logger.Debug().Msgf("found v0 configuration at %s with merge method %s", configV0Path, config.Merge.Method)

		fc.Config = config
		return fc, nil
	}

	fc.Error = errors.New("Unable to find valid v1 or v0 configuration")
	return fc, nil
}

// fetchConfigContents returns a nil slice if there is no configuration file
func (cf *ConfigFetcher) fetchConfigContents(ctx context.Context, client *github.Client, owner, repo, ref, configPath string) ([]byte, error) {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Str("path", configPath).Str("ref", ref).Msg("Attempting to fetch configuration definition")

	opts := &github.RepositoryContentGetOptions{
		Ref: ref,
	}

	file, _, _, err := client.Repositories.GetContents(ctx, owner, repo, configPath, opts)
	if err != nil {
		if rerr, ok := err.(*github.ErrorResponse); ok && rerr.Response.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "failed to fetch content of %q", configPath)
	}

	// file will be nil if the ref contains a directory at the expected file path
	if file == nil {
		return nil, nil
	}

	content, err := file.GetContent()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to decode content of %q", configPath)
	}

	return []byte(content), nil
}

func (cf *ConfigFetcher) unmarshalConfig(bytes []byte) (*Config, error) {
	var config Config
	if err := yaml.UnmarshalStrict(bytes, &config); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal configuration")
	}

	if config.Version != 1 {
		return nil, errors.Errorf("unexpected version '%d', expected 1", config.Version)
	}

	return &config, nil
}

func (cf *ConfigFetcher) unmarshalConfigV0(bytes []byte) (*Config, error) {
	var configv0 ConfigV0
	var config Config
	if err := yaml.UnmarshalStrict(bytes, &configv0); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal v0 configuration")
	}

	switch configv0.Mode {
	case ModeWhitelistV0:
		config = Config{
			Version: 1,
			Update: UpdateConfig{
				Whitelist: Signals{
					Labels: []string{"update me", "Update Me", "UPDATE ME", "update-me", "Update-Me", "UPDATE-ME", "update_me", "Update_Me", "UPDATE_ME"},
				},
			},
			Merge: MergeConfig{
				Whitelist: Signals{
					Labels: []string{"merge when ready", "Merge When Ready", "MERGE WHEN READY", "merge-when-ready", "Merge-When-Ready", "MERGE-WHEN-READY", "merge_when_ready", "Merge_When_Ready", "MERGE_WHEN_READY"},
				},
				DeleteAfterMerge: configv0.DeleteAfterMerge,
				Method:           configv0.Strategy,
				Options: map[MergeMethod]MergeOption{
					configv0.Strategy: {SummarizeCommits},
				},
			},
		}
	case ModeBlacklistV0:
		config = Config{
			Version: 1,
			Update: UpdateConfig{
				Whitelist: Signals{
					Labels: []string{"update me", "Update Me", "UPDATE ME", "update-me", "Update-Me", "UPDATE-ME", "update_me", "Update_Me", "UPDATE_ME"},
				},
			},
			Merge: MergeConfig{
				Blacklist: Signals{
					Labels: []string{"do not merge", "Do Not Merge", "DO NOT MERGE", "wip", "WIP", "do-not-merge", "Do-Not-Merge", "DO-NOT-MERGE", "do_not_merge", "Do_Not_Merge", "DO_NOT_MERGE"},
				},
				DeleteAfterMerge: configv0.DeleteAfterMerge,
				Method:           configv0.Strategy,
				Options: map[MergeMethod]MergeOption{
					configv0.Strategy: {SummarizeCommits},
				},
			},
		}
	case ModeBodyV0:
		config = Config{
			Version: 1,
			Update: UpdateConfig{
				Whitelist: Signals{
					Labels: []string{"update me", "Update Me", "UPDATE ME", "update-me", "Update-Me", "UPDATE-ME", "update_me", "Update_Me", "UPDATE_ME"},
				},
			},
			Merge: MergeConfig{
				Whitelist: Signals{
					CommentSubstrings: []string{"==MERGE_WHEN_READY=="},
				},
				DeleteAfterMerge: configv0.DeleteAfterMerge,
				Method:           configv0.Strategy,
				Options: map[MergeMethod]MergeOption{
					configv0.Strategy: {PullRequestBody},
				},
			},
		}
	default:
	}

	return &config, nil
}
