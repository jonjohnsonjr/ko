package steve

import (
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type Interface interface {
	Image() (v1.Image, error)
	ImageIndex() (v1.ImageIndex, error)
	Type() types.MediaType
}

func Image(img v1.Image) (Interface, error) {
	mt, err := img.MediaType()
	return steve{
		image:     img,
		mediaType: mt,
	}, err
}

func Index(idx v1.ImageIndex) (Interface, error) {
	mt, err := idx.MediaType()
	return steve{
		index:     idx,
		mediaType: mt,
	}, err
}

type steve struct {
	image     v1.Image
	index     v1.ImageIndex
	mediaType types.MediaType
}

func (s steve) Image() (v1.Image, error) {
	return s.image, nil
}

func (s steve) ImageIndex() (v1.ImageIndex, error) {
	return s.index, nil
}

func (s steve) Type() types.MediaType {
	return s.mediaType
}
