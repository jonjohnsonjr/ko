// Copyright 2018 Google LLC All Rights Reserved.
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

package build

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
)

const dockerprefix = "docker://"

type dockerbuild struct{}

// NewDocker returns a build.Interface implementation that:
//  1. builds Dockerfiles TODO
//  2. containerizes the binary on a suitable base,
func NewDocker() (Interface, error) {
	return &dockerbuild{}, nil
}

// IsSupportedReference implements build.Interface
//
// Only valid importpaths that provide commands (i.e., are "package main") are
// supported.
func (db *dockerbuild) IsSupportedReference(s string) bool {
	return strings.HasPrefix(s, dockerprefix)
}

// Build implements build.Interface
func (db *dockerbuild) Build(s string) (v1.Image, error) {
	dockerfile := strings.TrimPrefix(s, dockerprefix)
	tag := "ko.local" + uuid.New().String()
	path := "." // TODO
	cmd := exec.Command("docker", "build", "-f", dockerfile, "-t", tag, path)
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	var output bytes.Buffer
	cmd.Stdout = &output

	log.Printf("Building %s", dockerfile)
	if err := cmd.Run(); err != nil {
		log.Printf("Unexpected error running \"docker build\": %v\n%v", err, output.String())
		return nil, err
	}

	ref, err := name.ParseReference(tag)
	if err != nil {
		return nil, err
	}
	return daemon.Image(ref)
}
