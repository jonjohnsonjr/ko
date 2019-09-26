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

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/ko/pkg/build"
	"github.com/google/ko/pkg/commands/options"
	"github.com/google/ko/pkg/publish"
	"github.com/google/ko/pkg/resolve"
)

var singleton = &kopp{imgs: make(map[string]name.Reference)}

type Request struct {
	Uri string `json:"uri"`
}

type Response struct {
	Uri       string `json:"uri"`
	Reference string `json:"reference"`
}

type kopp struct {
	imgs map[string]name.Reference
}

func (k *kopp) Publish(img v1.Image, s string) (name.Reference, error) {
	if ref, ok := k.imgs[s]; ok {
		return ref, nil
	}
	return nil, fmt.Errorf("where'd you get this thing: %v", s)
}

func (k *kopp) IsSupportedReference(s string) bool {
	if !strings.HasPrefix(s, "ko-") {
		return false
	}
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 {
		return false
	}
	return true
}

func (k *kopp) Build(s string) (v1.Image, error) {
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("not a reference: %s")
	}

	req := Request{
		Uri: parts[1],
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(parts[0], "build")

	var output bytes.Buffer
	cmd.Stderr = os.Stderr
	cmd.Stdout = &output
	cmd.Stdin = bytes.NewBuffer(b)

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	resp := Response{}
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		return nil, err
	}

	ref, err := name.ParseReference(resp.Reference)
	if err != nil {
		return nil, err
	}
	return remote.Image(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

func makeBuilder(bo *options.BuildOptions) (build.Interface, error) {
	var innerBuilder build.Interface
	innerBuilder = singleton
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

func makePublisher(no *options.NameOptions, lo *options.LocalOptions, ta *options.TagsOptions) (publish.Interface, error) {
	return publish.NewCaching(singleton)
}

// resolvedFuture represents a "future" for the bytes of a resolved file.
type resolvedFuture chan []byte

func resolveFilesToWriter(builder build.Interface, publisher publish.Interface, fo *options.FilenameOptions, so *options.SelectorOptions, sto *options.StrictOptions, out io.WriteCloser) {
	defer out.Close()

	// By having this as a channel, we can hook this up to a filesystem
	// watcher and leave `fs` open to stream the names of yaml files
	// affected by code changes (including the modification of existing or
	// creation of new yaml files).
	fs := options.EnumerateFiles(fo)

	// This tracks filename -> []importpath
	var sm sync.Map

	var errCh chan error

	var futures []resolvedFuture
	for {
		// Each iteration, if there is anything in the list of futures,
		// listen to it in addition to the file enumerating channel.
		// A nil channel is never available to receive on, so if nothing
		// is available, this will result in us exclusively selecting
		// on the file enumerating channel.
		var bf resolvedFuture
		if len(futures) > 0 {
			bf = futures[0]
		} else if fs == nil {
			// There are no more files to enumerate and the futures
			// have been drained, so quit.
			break
		}

		select {
		case f, ok := <-fs:
			if !ok {
				// a nil channel is never available to receive on.
				// This allows us to drain the list of in-process
				// futures without this case of the select winning
				// each time.
				fs = nil
				break
			}

			// Make a new future to use to ship the bytes back and append
			// it to the list of futures (see comment below about ordering).
			ch := make(resolvedFuture)
			futures = append(futures, ch)

			// Kick off the resolution that will respond with its bytes on
			// the future.
			go func(f string) {
				defer close(ch)
				// Record the builds we do via this builder.
				recordingBuilder := &build.Recorder{
					Builder: builder,
				}
				b, err := resolveFile(f, recordingBuilder, publisher, so, sto)
				if err != nil {
					// Don't let build errors disrupt the watch.
					lg := log.Fatalf
					lg("error processing import paths in %q: %v", f, err)
					return
				}
				// Associate with this file the collection of binary import paths.
				sm.Store(f, recordingBuilder.ImportPaths)
				ch <- b
			}(f)

		case b, ok := <-bf:
			// Once the head channel returns something, dequeue it.
			// We listen to the futures in order to be respectful of
			// the kubectl apply ordering, which matters!
			futures = futures[1:]
			if ok {
				// Write the next body and a trailing delimiter.
				// We write the delimeter LAST so that when streamed to
				// kubectl it knows that the resource is complete and may
				// be applied.
				out.Write(append(b, []byte("\n---\n")...))
			}

		case err := <-errCh:
			log.Fatalf("Error watching dependencies: %v", err)
		}
	}
}

func resolveFile(f string, builder build.Interface, pub publish.Interface, so *options.SelectorOptions, sto *options.StrictOptions) (b []byte, err error) {
	if f == "-" {
		b, err = ioutil.ReadAll(os.Stdin)
	} else {
		b, err = ioutil.ReadFile(f)
	}
	if err != nil {
		return nil, err
	}

	if so.Selector != "" {
		b, err = resolve.FilterBySelector(b, so.Selector)
		if err != nil {
			return nil, err
		}
	}

	return resolve.ImageReferences(b, sto.Strict, builder, pub)
}
