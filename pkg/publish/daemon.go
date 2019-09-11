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

package publish

import (
	"fmt"
	"log"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/ko/pkg/build"
)

const (
	// LocalDomain is a sentinel "registry" that represents side-loading images into the daemon.
	LocalDomain = "ko.local"
)

// demon is intentionally misspelled to avoid name collision (and drive Jon nuts).
type demon struct {
	namer Namer
	tags  []string
}

// NewDaemon returns a new publish.Interface that publishes images to a container daemon.
func NewDaemon(namer Namer, tags []string) Interface {
	return &demon{namer, tags}
}

func toImage(br build.Result) (v1.Image, error) {
	mt, err := br.MediaType()
	if err != nil {
		return nil, err
	}

	switch mt {
	case types.OCIImageIndex, types.DockerManifestList:
		idx, ok := br.(v1.ImageIndex)
		if !ok {
			return nil, fmt.Errorf("failed to interpret result as index: %v", br)
		}
		im, err := idx.IndexManifest()
		if err != nil {
			return nil, err
		}
		return idx.Image(im.Manifests[0].Digest)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		img, ok := br.(v1.Image)
		if !ok {
			return nil, fmt.Errorf("failed to interpret result as image: %v", br)
		}
		return img, nil
	default:
		return nil, fmt.Errorf("result image media type: %s", mt)
	}
}

// Publish implements publish.Interface
func (d *demon) Publish(br build.Result, s string) (name.Reference, error) {
	// https://github.com/google/go-containerregistry/issues/212
	s = strings.ToLower(s)

	// TODO: Do we ever want to write multiple images to the daemon?
	img, err := toImage(br)
	if err != nil {
		return nil, err
	}

	h, err := img.Digest()
	if err != nil {
		return nil, err
	}

	digestTag, err := name.NewTag(fmt.Sprintf("%s/%s:%s", LocalDomain, d.namer(s), h.Hex))
	if err != nil {
		return nil, err
	}

	log.Printf("Loading %v", digestTag)
	if _, err := daemon.Write(digestTag, img); err != nil {
		return nil, err
	}
	log.Printf("Loaded %v", digestTag)

	for _, tagName := range d.tags {
		log.Printf("Adding tag %v", tagName)
		tag, err := name.NewTag(fmt.Sprintf("%s/%s:%s", LocalDomain, d.namer(s), tagName))
		if err != nil {
			return nil, err
		}

		err = daemon.Tag(digestTag, tag)

		if err != nil {
			return nil, err
		}
		log.Printf("Added tag %v", tagName)
	}

	return &digestTag, nil
}
