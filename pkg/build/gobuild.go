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
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	gb "go/build"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/ko/pkg/steve"
)

const (
	appDir             = "/ko-app"
	defaultAppFilename = "ko-app"
)

// GetBase takes an importpath and returns a steve.Interface.
type GetBase func(string) (steve.Interface, error)
type builder func(string, v1.Platform, bool) (string, error)

type gobuild struct {
	getBase              GetBase
	creationTime         v1.Time
	build                builder
	disableOptimizations bool
}

// Option is a functional option for NewGo.
type Option func(*gobuildOpener) error

type gobuildOpener struct {
	getBase              GetBase
	creationTime         v1.Time
	build                builder
	disableOptimizations bool
}

func (gbo *gobuildOpener) Open() (Interface, error) {
	if gbo.getBase == nil {
		return nil, errors.New("a way of providing base images must be specified, see build.WithBaseImages")
	}
	return &gobuild{
		getBase:              gbo.getBase,
		creationTime:         gbo.creationTime,
		build:                gbo.build,
		disableOptimizations: gbo.disableOptimizations,
	}, nil
}

// NewGo returns a build.Interface implementation that:
//  1. builds go binaries named by importpath,
//  2. containerizes the binary on a suitable base,
func NewGo(options ...Option) (Interface, error) {
	gbo := &gobuildOpener{
		build: build,
	}

	for _, option := range options {
		if err := option(gbo); err != nil {
			return nil, err
		}
	}
	return gbo.Open()
}

// IsSupportedReference implements build.Interface
//
// Only valid importpaths that provide commands (i.e., are "package main") are
// supported.
func (*gobuild) IsSupportedReference(s string) bool {
	p, err := gb.Import(s, gb.Default.GOPATH, gb.ImportComment)
	if err != nil {
		return false
	}
	return p.IsCommand()
}

func build(ip string, p v1.Platform, disableOptimizations bool) (string, error) {
	tmpDir, err := ioutil.TempDir("", "ko")
	if err != nil {
		return "", err
	}
	file := filepath.Join(tmpDir, "out")

	args := make([]string, 0, 6)
	args = append(args, "build")
	if disableOptimizations {
		// Disable optimizations (-N) and inlining (-l).
		args = append(args, "-gcflags", "all=-N -l")
	}
	args = append(args, "-o", file)
	args = append(args, ip)
	cmd := exec.Command("go", args...)

	// Last one wins
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+p.OS, "GOARCH=", p.Architecture)

	var output bytes.Buffer
	cmd.Stderr = &output
	cmd.Stdout = &output

	log.Printf("Building %s", ip)
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		log.Printf("Unexpected error running \"go build\": %v\n%v", err, output.String())
		return "", err
	}
	return file, nil
}

func appFilename(importpath string) string {
	base := filepath.Base(importpath)

	// If we fail to determine a good name from the importpath then use a
	// safe default.
	if base == "." || base == string(filepath.Separator) {
		return defaultAppFilename
	}

	return base
}

func tarAddDirectories(tw *tar.Writer, dir string) error {
	if dir == "." || dir == string(filepath.Separator) {
		return nil
	}

	// Write parent directories first
	if err := tarAddDirectories(tw, filepath.Dir(dir)); err != nil {
		return err
	}

	// write the directory header to the tarball archive
	if err := tw.WriteHeader(&tar.Header{
		Name:     dir,
		Typeflag: tar.TypeDir,
		// Use a fixed Mode, so that this isn't sensitive to the directory and umask
		// under which it was created. Additionally, windows can only set 0222,
		// 0444, or 0666, none of which are executable.
		Mode: 0555,
	}); err != nil {
		return err
	}

	return nil
}

func tarBinary(name, binary string) (*bytes.Buffer, error) {
	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	// write the parent directories to the tarball archive
	if err := tarAddDirectories(tw, filepath.Dir(name)); err != nil {
		return nil, err
	}

	file, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	header := &tar.Header{
		Name:     name,
		Size:     stat.Size(),
		Typeflag: tar.TypeReg,
		// Use a fixed Mode, so that this isn't sensitive to the directory and umask
		// under which it was created. Additionally, windows can only set 0222,
		// 0444, or 0666, none of which are executable.
		Mode: 0555,
	}
	// write the header to the tarball archive
	if err := tw.WriteHeader(header); err != nil {
		return nil, err
	}
	// copy the file data to the tarball
	if _, err := io.Copy(tw, file); err != nil {
		return nil, err
	}

	return buf, nil
}

func kodataPath(s string) (string, error) {
	p, err := gb.Import(s, gb.Default.GOPATH, gb.ImportComment)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Dir, "kodata"), nil
}

// Where kodata lives in the image.
const kodataRoot = "/var/run/ko"

func tarKoData(importpath string) (*bytes.Buffer, error) {
	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	root, err := kodataPath(importpath)
	if err != nil {
		return nil, err
	}

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if path == root {
			// Add an entry for /var/run/ko
			return tw.WriteHeader(&tar.Header{
				Name:     kodataRoot,
				Typeflag: tar.TypeDir,
			})
		}
		if err != nil {
			return err
		}
		// Skip other directories.
		if info.Mode().IsDir() {
			return nil
		}

		// Chase symlinks.
		info, err = os.Stat(path)
		if err != nil {
			return err
		}

		// Open the file to copy it into the tarball.
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		// Copy the file into the image tarball.
		newPath := filepath.Join(kodataRoot, path[len(root):])
		if err := tw.WriteHeader(&tar.Header{
			Name:     newPath,
			Size:     info.Size(),
			Typeflag: tar.TypeReg,
			// Use a fixed Mode, so that this isn't sensitive to the directory and umask
			// under which it was created. Additionally, windows can only set 0222,
			// 0444, or 0666, none of which are executable.
			Mode: 0555,
		}); err != nil {
			return err
		}
		_, err = io.Copy(tw, file)
		return err
	})
	if err != nil {
		return nil, err
	}

	return buf, nil
}

func (gb *gobuild) buildImage(s string, base v1.Image) (v1.Image, error) {
	baseCfg, err := base.ConfigFile()
	if err != nil {
		return nil, err
	}

	p := v1.Platform{
		Architecture: baseCfg.Architecture,
		OS:           baseCfg.OS,
	}

	// Do the build into a temporary file.
	file, err := gb.build(s, p, gb.disableOptimizations)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(file))

	var layers []v1.Layer
	// Create a layer from the kodata directory under this import path.
	dataLayerBuf, err := tarKoData(s)
	if err != nil {
		return nil, err
	}
	dataLayerBytes := dataLayerBuf.Bytes()
	dataLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return ioutil.NopCloser(bytes.NewBuffer(dataLayerBytes)), nil
	})
	if err != nil {
		return nil, err
	}
	layers = append(layers, dataLayer)

	appPath := filepath.Join(appDir, appFilename(s))

	// Construct a tarball with the binary and produce a layer.
	binaryLayerBuf, err := tarBinary(appPath, file)
	if err != nil {
		return nil, err
	}
	binaryLayerBytes := binaryLayerBuf.Bytes()
	binaryLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return ioutil.NopCloser(bytes.NewBuffer(binaryLayerBytes)), nil
	})
	if err != nil {
		return nil, err
	}
	layers = append(layers, binaryLayer)

	// Augment the base image with our application layer.
	withApp, err := mutate.AppendLayers(base, layers...)
	if err != nil {
		return nil, err
	}

	// Start from a copy of the base image's config file, and set
	// the entrypoint to our app.
	cfg, err := withApp.ConfigFile()
	if err != nil {
		return nil, err
	}

	cfg = cfg.DeepCopy()
	cfg.Config.Entrypoint = []string{appPath}
	cfg.Config.Env = append(cfg.Config.Env, "KO_DATA_PATH="+kodataRoot)

	image, err := mutate.Config(withApp, cfg.Config)
	if err != nil {
		return nil, err
	}

	empty := v1.Time{}
	if gb.creationTime != empty {
		return mutate.CreatedAt(image, gb.creationTime)
	}
	return image, nil
}

// Build implements build.Interface
func (gb *gobuild) Build(s string) (steve.Interface, error) {
	// Determine the appropriate base image for this import path.
	base, err := gb.getBase(s)
	if err != nil {
		return nil, err
	}

	switch base.Type() {
	case types.OCIImageIndex, types.DockerManifestList:
		idx, err := base.ImageIndex()
		if err != nil {
			return nil, err
		}
		built, err := newIndex(idx)
		if err != nil {
			return nil, err
		}
		im, err := idx.IndexManifest()
		if err != nil {
			return nil, err
		}
		for _, desc := range im.Manifests {
			baseImage, err := idx.Image(desc.Digest)
			if err != nil {
				return nil, err
			}
			img, err := gb.buildImage(s, baseImage)
			if err != nil {
				return nil, err
			}
			if err := built.AddImage(img, desc); err != nil {
				return nil, err
			}
		}
		return steve.Index(built)
	default:
		baseImage, err := base.Image()
		if err != nil {
			return nil, err
		}
		img, err := gb.buildImage(s, baseImage)
		if err != nil {
			return nil, err
		}
		return steve.Image(img)
	}

	return nil, fmt.Errorf("oops")
}

type index struct {
	manifest *v1.IndexManifest
	images   map[v1.Hash]v1.Image
}

func newIndex(original v1.ImageIndex) (*index, error) {
	manifest, err := original.IndexManifest()
	if err != nil {
		return nil, err
	}

	// Clear the manifests from the original, we'll populate that in AddImage.
	manifest.Manifests = []v1.Descriptor{}
	return &index{
		manifest: manifest,
		images:   make(map[v1.Hash]v1.Image),
	}, nil
}

// AddImage adds the newly built image to the index and updates the digest and
// size of the descriptor.
func (i *index) AddImage(img v1.Image, original v1.Descriptor) error {
	h, err := img.Digest()
	if err != nil {
		return err
	}
	i.images[h] = img

	b, err := img.RawManifest()
	if err != nil {
		return err
	}

	updated := original
	updated.Digest = h
	updated.Size = int64(len(b))

	i.manifest.Manifests = append(i.manifest.Manifests, updated)
	return nil
}

func (i *index) MediaType() (types.MediaType, error) {
	mt := i.manifest.MediaType
	if string(mt) != "" {
		return mt, nil
	}
	return types.OCIImageIndex, nil
}

func (i *index) Digest() (v1.Hash, error) {
	return partial.Digest(i)
}

func (i *index) IndexManifest() (*v1.IndexManifest, error) {
	return i.manifest, nil
}

func (i *index) RawManifest() ([]byte, error) {
	im, err := i.IndexManifest()
	if err != nil {
		return nil, err
	}
	return json.Marshal(im)
}

func (i *index) Image(h v1.Hash) (v1.Image, error) {
	if img, ok := i.images[h]; ok {
		return img, nil
	}
	return nil, fmt.Errorf("no image with hash %s", h)
}

func (i *index) ImageIndex(h v1.Hash) (v1.ImageIndex, error) {
	return nil, fmt.Errorf("no index with hash %s", h)
}
