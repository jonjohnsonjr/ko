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

package options

import (
	"github.com/spf13/cobra"
)

// PlatformOptions represents options for the image platform.
type PlatformOptions struct {
	// Platform is the platform of the image to pull (if the base image is an
	// image index).
	Platform string
}

func AddPlatformArg(cmd *cobra.Command, po *PlatformOptions) {
	cmd.Flags().StringVar(&po.Platform, "platform", po.Platform,
		"The platform to use for pulling the base image. TODO: Specify format.")
}
