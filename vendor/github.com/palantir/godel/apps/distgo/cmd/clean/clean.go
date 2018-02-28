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

package clean

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"

	"github.com/palantir/godel/apps/distgo/cmd/build"
	"github.com/palantir/godel/apps/distgo/cmd/dist"
	"github.com/palantir/godel/apps/distgo/params"
	"github.com/palantir/godel/pkg/osarch"
)

func Products(products []string, cfg params.Project, dryRun bool, wd string, stdout io.Writer) error {
	buildSpecsWithDeps, err := build.SpecsWithDepsForArgs(cfg, products, wd)
	if err != nil {
		return err
	}
	for _, specWithDeps := range buildSpecsWithDeps {
		if err := Run(specWithDeps, dryRun, stdout); err != nil {
			return errors.Wrapf(err, "failed to clean %s", specWithDeps.Spec.ProductName)
		}
	}
	return nil
}

type pathInfo struct {
	// path to the "root" output directory (output directory of "bin" or "dist" tasks).
	rootDir string
	// true if this path is a directory, false if it is a file
	isDir bool
}

// Run cleans the outputs generated by the specified product.
func Run(buildSpecWithDeps params.ProductBuildSpecWithDeps, dryRun bool, stdout io.Writer) error {
	// outputDir -> product -> osArchs
	outputDirToMap := make(map[string]map[string][]osarch.OSArch)

	// add primary product
	outputDirToMap[path.Join(buildSpecWithDeps.Spec.ProjectDir, buildSpecWithDeps.Spec.Build.OutputDir)] = map[string][]osarch.OSArch{
		buildSpecWithDeps.Spec.ProductName: buildSpecWithDeps.Spec.Build.OSArchs,
	}

	// add dependent products
	for _, currDepSpec := range buildSpecWithDeps.Deps {
		currMap, ok := outputDirToMap[path.Join(buildSpecWithDeps.Spec.ProjectDir, currDepSpec.Build.OutputDir)]
		if !ok {
			currMap = make(map[string][]osarch.OSArch)
			outputDirToMap[path.Join(buildSpecWithDeps.Spec.ProjectDir, currDepSpec.Build.OutputDir)] = currMap
		}
		currMap[currDepSpec.ProductName] = currDepSpec.Build.OSArchs
	}

	// map of paths to remove. Value is true if currPath is a directory, false otherwise.
	removePaths := make(map[string]pathInfo)

	// remove binaries for specified products
	for outputDir, products := range outputDirToMap {
		removed, err := cleanBinOutput(outputDir, products)
		if err != nil {
			return errors.Wrapf(err, "failed to remove")
		}
		for k := range removed {
			removePaths[k] = pathInfo{
				rootDir: outputDir,
				isDir:   false,
			}
		}
	}

	// remove dists for product
	buildSpecWithDeps.Spec.ProductVersion = ".+"
	for _, currDist := range buildSpecWithDeps.Spec.Dist {
		var distDirRegexps []*regexp.Regexp
		distDirRegexps = append(distDirRegexps, regexp.MustCompile(fmt.Sprintf("%s-.+", buildSpecWithDeps.Spec.ProductName)))
		for _, currDistPath := range dist.FullArtifactsPaths(dist.ToDister(currDist.Info), buildSpecWithDeps.Spec, currDist) {
			distDirRegexps = append(distDirRegexps, regexp.MustCompile(path.Base(currDistPath)))
		}
		distDirFiles, err := ioutil.ReadDir(path.Join(buildSpecWithDeps.Spec.ProjectDir, currDist.OutputDir))
		if err != nil {
			continue
		}
		for _, currDistFile := range distDirFiles {
			for _, currRegexp := range distDirRegexps {
				if currRegexp.MatchString(currDistFile.Name()) {
					removePaths[path.Join(buildSpecWithDeps.Spec.ProjectDir, currDist.OutputDir, currDistFile.Name())] = pathInfo{
						rootDir: path.Join(buildSpecWithDeps.Spec.ProjectDir, currDist.OutputDir),
						isDir:   currDistFile.IsDir(),
					}
					break
				}
			}
		}
	}

	var sortedPaths []string
	for k := range removePaths {
		sortedPaths = append(sortedPaths, k)
	}
	sort.Strings(sortedPaths)

	prefix := "[DRY RUN]"
	if dryRun {
		fmt.Fprintf(stdout, "%s Clean %s will remove paths:\n", prefix, buildSpecWithDeps.Spec.ProductName)
	}

	// stores all of the paths that were removed/marked for removal
	removedPaths := make(map[string]struct{})
	for _, currPath := range sortedPaths {
		pathInfo := removePaths[currPath]
		if dryRun {
			fmt.Fprintf(stdout, "%s     %s\n", prefix, currPath)
		} else {
			// if target path exists, attempt to remove it
			if _, err := os.Stat(currPath); err == nil {
				if pathInfo.isDir {
					if err := os.RemoveAll(currPath); err != nil {
						return errors.Wrapf(err, "failed to remove directory %s", currPath)
					}
				} else {
					if err := os.Remove(currPath); err != nil {
						return errors.Wrapf(err, "failed to remove file %s", currPath)
					}
				}
			}
		}
		removedPaths[currPath] = struct{}{}

		// verify that current path is direct descendant of root directory
		if !strings.Contains(currPath, pathInfo.rootDir) {
			return errors.Errorf("root dir path %s does not occur in %s", pathInfo.rootDir, currPath)
		}

		// for each parent directory between the removed path and the root, check if removal caused it to become empty.
		// If so, remove it and continue the process.
		currParentDir := currPath
		for {
			currParentDir = path.Dir(currParentDir)
			if currParentDir == pathInfo.rootDir {
				break
			}
			if _, err := os.Stat(currParentDir); os.IsNotExist(err) {
				// nothing to do if parent directory does not exist
				break
			}
			removed, err := removeDirIfEmpty(currParentDir, removedPaths, dryRun)
			if err != nil {
				return err
			}
			if !removed {
				// if there was no error and directory was not removed, nothing more to do
				break
			}
			if dryRun {
				fmt.Fprintf(stdout, "%s     %s\n", prefix, currParentDir)
			}
		}
		// remove root directory if it is now empty
		rootDirRemoved, err := removeDirIfEmpty(pathInfo.rootDir, removedPaths, dryRun)
		if err != nil {
			return err
		}
		if rootDirRemoved && dryRun {
			fmt.Fprintf(stdout, "%s     %s\n", prefix, pathInfo.rootDir)
		}
	}
	return nil
}

// Removes the given path (which must be a directory) if it exists and is empty. Returns true if the directory was
// removed, false otherwise. Returns an error if the provided path was not a directory of if there was an error reading
// or removing it.
func removeDirIfEmpty(dirPath string, removedPaths map[string]struct{}, dryRun bool) (bool, error) {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return false, nil
	}
	dirFiles, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return false, errors.Wrapf(err, "failed to read directory: %s", dirPath)
	}

	// if this is a dry run, determine the files that "should" exist
	if dryRun {
		var dryRunDirFiles []os.FileInfo
		for _, currDirFile := range dirFiles {
			if _, ok := removedPaths[path.Join(dirPath, currDirFile.Name())]; ok {
				// path is a path marked for removal: do not consider it
				continue
			}
			dryRunDirFiles = append(dryRunDirFiles, currDirFile)
		}
		dirFiles = dryRunDirFiles
	}

	if len(dirFiles) != 0 {
		// if directory contains files, nothing to do: do not remove
		return false, nil
	}

	if !dryRun {
		// if this is not a dry run, actually perform the removal
		if err := os.RemoveAll(dirPath); err != nil {
			return false, errors.Wrapf(err, "failed to remove directory %s", dirPath)
		}
	}

	removedPaths[dirPath] = struct{}{}
	return true, nil
}

func cleanBinOutput(outputDir string, products map[string][]osarch.OSArch) (map[string]struct{}, error) {
	removedPaths := make(map[string]struct{})
	osArchToProducts := make(map[string]map[string]struct{})
	for currProduct, prodOSArchs := range products {
		for _, currOSArch := range prodOSArchs {
			currProductsMap, ok := osArchToProducts[currOSArch.String()]
			if !ok {
				currProductsMap = make(map[string]struct{})
				osArchToProducts[currOSArch.String()] = currProductsMap
			}
			currProductsMap[currProduct] = struct{}{}
		}
	}

	outputDirFiles, err := ioutil.ReadDir(outputDir)
	if err != nil {
		// nothing to do if output directory does not exist or cannot be read
		return nil, nil
	}
	for _, outputDirFile := range outputDirFiles {
		if !outputDirFile.IsDir() {
			continue
		}
		// directory in top-level output directory: could be a tag directory. Examine all os-arch directories within it.
		currTagDirPath := path.Join(outputDir, outputDirFile.Name())
		tagDirFiles, err := ioutil.ReadDir(currTagDirPath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read directory %s", currTagDirPath)
		}
		for _, tagDirFile := range tagDirFiles {
			if !tagDirFile.IsDir() {
				continue
			}
			products, ok := osArchToProducts[tagDirFile.Name()]
			if !ok {
				continue
			}
			// at least one product of this OS/architecture exists: examine contents
			currOSArchDirPath := path.Join(currTagDirPath, tagDirFile.Name())
			osArchDirFiles, err := ioutil.ReadDir(currOSArchDirPath)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to read directory %s", currOSArchDirPath)
			}
			for _, osArchDirFile := range osArchDirFiles {
				if _, ok := products[osArchDirFile.Name()]; !ok {
					continue
				}
				binToRemovePath := path.Join(currOSArchDirPath, osArchDirFile.Name())
				removedPaths[binToRemovePath] = struct{}{}
			}
		}
	}
	return removedPaths, nil
}
