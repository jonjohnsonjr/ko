// Copyright 2019 Google LLC All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/ko/pkg/build"
	"github.com/google/ko/pkg/commands/options"
	"github.com/google/ko/pkg/publish"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TODO: share this stuff
func main() {
	// Parent command to which all subcommands are added.
	cmds := &cobra.Command{
		Use:   "ko-go",
		Short: "A ko builder for go.",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}

	cmds.AddCommand(NewCmdBuild())

	if err := cmds.Execute(); err != nil {
		log.Fatalf("error during command execution: %v", err)
	}
}

type Options struct {
	KoDockerRepo string `json:"ko_docker_repo"`
}

type Request struct {
	Uri     string  `json:"uri"`
	Options Options `json:"options"`
}

type Response struct {
	Uri       string `json:"uri"`
	Reference string `json:"reference"`
}

func NewCmdBuild() *cobra.Command {
	lo := &options.LocalOptions{}
	no := &options.NameOptions{}
	ta := &options.TagsOptions{}
	bo := &options.BuildOptions{}

	// TODO
	watch := false
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Builds and publishes an image given an import path.",
		Run: func(cmd *cobra.Command, args []string) {
			builder, err := makeBuilder(bo)
			if err != nil {
				log.Fatalf("error creating builder: %v", err)
			}
			publisher, err := makePublisher(no, lo, ta)
			if err != nil {
				log.Fatalf("error creating publisher: %v", err)
			}

			if err := kobuild(builder, publisher, os.Stdin, os.Stdout); err != nil {
				log.Fatal(err)
			}
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "watch for file changes")

	options.AddLocalArg(cmd, lo)
	options.AddNamingArgs(cmd, no)
	options.AddTagsArg(cmd, ta)
	options.AddBuildOptions(cmd, bo)

	return cmd
}

func kobuild(b build.Interface, pub publish.Interface, stdin io.Reader, stdout io.Writer) error {
	in := bufio.NewReader(stdin)
	for {
		line, err := in.ReadString('\n')
		if err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("error when reading from stdin: %v", err)
		}

		req := Request{}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return err
		}
		log.Printf("%+v", req)
		importpath := req.Uri
		if !b.IsSupportedReference(importpath) {
			return fmt.Errorf("importpath %q is not supported", importpath)
		}

		img, err := b.Build(importpath)
		if err != nil {
			return fmt.Errorf("error building %q: %v", importpath, err)
		}
		ref, err := pub.Publish(img, importpath)
		if err != nil {
			return fmt.Errorf("error publishing %s: %v", importpath, err)
		}
		resp := Response{
			Uri:       req.Uri,
			Reference: ref.String(),
		}
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, string(b))
	}
	return nil
}

func makePublisher(no *options.NameOptions, lo *options.LocalOptions, ta *options.TagsOptions) (publish.Interface, error) {
	// Create the publish.Interface that we will use to publish image references
	// to either a docker daemon or a container image registry.
	innerPublisher, err := func() (publish.Interface, error) {
		namer := options.MakeNamer(no)

		repoName := os.Getenv("KO_DOCKER_REPO")
		if lo.Local || repoName == publish.LocalDomain {
			return publish.NewDaemon(namer, ta.Tags), nil
		}
		if repoName == "" {
			return nil, errors.New("KO_DOCKER_REPO environment variable is unset")
		}
		_, err := name.NewRepository(repoName)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable KO_DOCKER_REPO=%q as repository: %v", repoName, err)
		}

		return publish.NewDefault(repoName,
			publish.WithAuthFromKeychain(authn.DefaultKeychain),
			publish.WithNamer(namer),
			publish.WithTags(ta.Tags),
			publish.Insecure(lo.InsecureRegistry))
	}()
	if err != nil {
		return nil, err
	}

	// Wrap publisher in a memoizing publisher implementation.
	return publish.NewCaching(innerPublisher)
}

func gobuildOptions(bo *options.BuildOptions) ([]build.Option, error) {
	creationTime, err := getCreationTime()
	if err != nil {
		return nil, err
	}
	opts := []build.Option{
		build.WithBaseImages(getBaseImage),
	}
	if creationTime != nil {
		opts = append(opts, build.WithCreationTime(*creationTime))
	}
	if bo.DisableOptimizations {
		opts = append(opts, build.WithDisabledOptimizations())
	}
	return opts, nil
}

func makeBuilder(bo *options.BuildOptions) (*build.Caching, error) {
	opt, err := gobuildOptions(bo)
	if err != nil {
		log.Fatalf("error setting up builder options: %v", err)
	}
	innerBuilder, err := build.NewGo(opt...)
	if err != nil {
		return nil, err
	}

	innerBuilder = build.NewLimiter(innerBuilder, bo.ConcurrentBuilds)

	// tl;dr Wrap builder in a caching builder.
	//
	// The caching builder should on Build calls:
	//  - Check for a valid Build future
	//    - if a valid Build future exists at the time of the request,
	//      then block on it.
	//    - if it does not, then initiate and record a Build future.
	//  - When import paths are "affected" by filesystem changes during a
	//    Watch, then invalidate their build futures *before* we put the
	//    affected yaml files onto the channel
	//
	// This will benefit the following key cases:
	// 1. When the same import path is referenced across multiple yaml files
	//    we can elide subsequent builds by blocking on the same image future.
	// 2. When an affected yaml file has multiple import paths (mostly unaffected)
	//    we can elide the builds of unchanged import paths.
	return build.NewCaching(innerBuilder)
}

var (
	defaultBaseImage   name.Reference
	baseImageOverrides map[string]name.Reference
)

func getBaseImage(s string) (v1.Image, error) {
	ref, ok := baseImageOverrides[s]
	if !ok {
		ref = defaultBaseImage
	}
	log.Printf("Using base %s for %s", ref, s)
	return remote.Image(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

func getCreationTime() (*v1.Time, error) {
	epoch := os.Getenv("SOURCE_DATE_EPOCH")
	if epoch == "" {
		return nil, nil
	}

	seconds, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("the environment variable SOURCE_DATE_EPOCH should be the number of seconds since January 1st 1970, 00:00 UTC, got: %v", err)
	}
	return &v1.Time{time.Unix(seconds, 0)}, nil
}

func init() {
	// If omitted, use this base image.
	viper.SetDefault("defaultBaseImage", "gcr.io/distroless/static:latest")
	viper.SetConfigName(".ko") // .yaml is implicit

	if override := os.Getenv("KO_CONFIG_PATH"); override != "" {
		viper.AddConfigPath(override)
	}

	viper.AddConfigPath("./")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Fatalf("error reading config file: %v", err)
		}
	}

	ref := viper.GetString("defaultBaseImage")
	dbi, err := name.ParseReference(ref)
	if err != nil {
		log.Fatalf("'defaultBaseImage': error parsing %q as image reference: %v", ref, err)
	}
	defaultBaseImage = dbi

	baseImageOverrides = make(map[string]name.Reference)
	overrides := viper.GetStringMapString("baseImageOverrides")
	for k, v := range overrides {
		bi, err := name.ParseReference(v)
		if err != nil {
			log.Fatalf("'baseImageOverrides': error parsing %q as image reference: %v", v, err)
		}
		baseImageOverrides[k] = bi
	}
}
