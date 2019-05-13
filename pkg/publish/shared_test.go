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
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/ko/pkg/steve"
)

type slowpublish struct {
	sleep time.Duration
}

// slowpublish implements Interface
var _ Interface = (*slowpublish)(nil)

func (sb *slowpublish) Publish(st steve.Interface, ref string) (name.Reference, error) {
	time.Sleep(sb.sleep)
	return makeRef()
}

func TestCaching(t *testing.T) {
	duration := 100 * time.Millisecond
	ref := "foo"

	sb := &slowpublish{duration}
	cb, _ := NewCaching(sb)

	previousDigest := "not-a-digest"
	// Each iteration, we test that the first publish is slow and subsequent
	// publishs are fast and return the same reference.  For each of these
	// iterations we use a new random image, which should invalidate the
	// cached reference from previous iterations.
	for idx := 0; idx < 3; idx++ {
		img, _ := random.Image(256, 8)
		st, _ := steve.Image(img)

		start := time.Now()
		ref1, err := cb.Publish(st, ref)
		if err != nil {
			t.Errorf("Publish() = %v", err)
		}
		end := time.Now()

		elapsed := end.Sub(start)
		if elapsed < duration {
			t.Errorf("Elapsed time %v, wanted >= %s", elapsed, duration)
		}
		d1 := ref1.String()

		if d1 == previousDigest {
			t.Errorf("Got same digest as previous iteration, wanted different: %v", d1)
		}
		previousDigest = d1

		start = time.Now()
		ref2, err := cb.Publish(st, ref)
		if err != nil {
			t.Errorf("Publish() = %v", err)
		}
		end = time.Now()

		elapsed = end.Sub(start)
		if elapsed >= duration {
			t.Errorf("Elapsed time %v, wanted < %s", elapsed, duration)
		}
		d2 := ref2.String()

		if d1 != d2 {
			t.Error("Got different references, wanted same")
		}
	}
}
