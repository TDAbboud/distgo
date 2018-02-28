// Copyright 2016 Palantir Technologies, Inc.
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

package dister

import (
	"os"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/palantir/distgo/distgo"
)

const ManualDistTypeName = "manual" // distribution that consists of a distribution whose output is created by the distribution script

type ManualDistConfig struct {
	// Extension is the extension used by the target output generated by the dist script: for example, "tgz",
	// "zip", etc. Extension is used to locate the output generated by the dist script. The output should be a file
	// of the form "{{product-name}}-{{version}}.{{Extension}}". If Extension is empty, it is assumed that the
	// output has no extension and is of the form "{{product-name}}-{{version}}".
	Extension string `yaml:"extension" json:"extension"`
}

func (cfg *ManualDistConfig) ToDister() distgo.Dister {
	return &manualDister{
		Extension: cfg.Extension,
	}
}

type manualDister struct {
	Extension string
}

func NewManualDisterFromConfig(cfgYML []byte) (distgo.Dister, error) {
	var disterCfg ManualDistConfig
	if err := yaml.Unmarshal(cfgYML, &disterCfg); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal YAML")
	}
	return disterCfg.ToDister(), nil
}

func (d *manualDister) TypeName() (string, error) {
	return ManualDistTypeName, nil
}

func (d *manualDister) Artifacts(renderedNameTemplate string) ([]string, error) {
	outputFileName := renderedNameTemplate
	if d.Extension != "" {
		outputFileName += "." + d.Extension
	}
	return []string{outputFileName}, nil
}

func (d *manualDister) RunDist(distID distgo.DistID, productTaskOutputInfo distgo.ProductTaskOutputInfo) ([]byte, error) {
	// manual dister does not perform any actions (all actions are preformed by script)
	return nil, nil
}

func (d *manualDister) GenerateDistArtifacts(distID distgo.DistID, productTaskOutputInfo distgo.ProductTaskOutputInfo, runDistResult []byte) error {
	artifactPaths, err := d.Artifacts(productTaskOutputInfo.Product.DistOutputInfos.DistInfos[distID].DistNameTemplateRendered)
	if err != nil {
		return err
	}
	if len(artifactPaths) != 1 {
		return errors.Errorf("manual distribution must produce a single artifact")
	}

	// manual dister depends on the script to generate the declared output -- verify that the output exists, and fail if it does not.
	fi, err := os.Stat(artifactPaths[0])
	if os.IsNotExist(err) {
		return errors.Wrapf(err, "expected output does not exist at %s", artifactPaths[0])
	}
	// output should not be a directory
	if fi.IsDir() {
		return errors.Errorf("output at %s is a directory", artifactPaths[0])
	}
	return nil
}
