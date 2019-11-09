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
	"os/exec"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/ko/pkg/commands/options"
	"github.com/spf13/cobra"
)

// TODO: share this stuff
func main() {
	// Parent command to which all subcommands are added.
	cmds := &cobra.Command{
		Use:   "ko-docker",
		Short: "A ko builder for Dockerfiles.",
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

	// TODO
	watch := false
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Builds and publishes an image given an import path.",
		Run: func(cmd *cobra.Command, args []string) {
			if err := kobuild(no, lo, os.Stdin, os.Stdout); err != nil {
				log.Fatal(err)
			}
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "watch for file changes")

	options.AddLocalArg(cmd, lo)
	options.AddNamingArgs(cmd, no)
	options.AddTagsArg(cmd, ta)

	return cmd
}

func kobuild(no *options.NameOptions, lo *options.LocalOptions, stdin io.Reader, stdout io.Writer) error {
	in := bufio.NewReader(stdin)
	for {
		line, err := in.ReadString('\n')
		if err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("error when reading from stdin: %v", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		req := Request{}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return fmt.Errorf("erroring unmarshalling: %v", err)
		}

		namer := options.MakeNamer(no)

		repoName := os.Getenv("KO_DOCKER_REPO")
		if repoName == "" {
			return errors.New("KO_DOCKER_REPO environment variable is unset")
		}
		_, err = name.NewRepository(repoName)
		if err != nil {
			return fmt.Errorf("failed to parse environment variable KO_DOCKER_REPO=%q as repository: %v", repoName, err)
		}

		path := req.Uri
		// TODO
		tag := "latest"
		ref := fmt.Sprintf("%s/%s:%s", repoName, namer("todo"), tag)
		cmd := exec.Command("docker", "build", "-t", ref, path)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker build: %v", err)
		}

		if !lo.Local {
			cmd := exec.Command("docker", "push", ref)
			cmd.Stderr = os.Stderr
			cmd.Stdout = os.Stderr

			if err := cmd.Run(); err != nil {
				return fmt.Errorf("docker push: %v", err)
			}
		}

		resp := Response{
			Uri:       req.Uri,
			Reference: ref,
		}
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, string(b))
	}
	return nil
}
