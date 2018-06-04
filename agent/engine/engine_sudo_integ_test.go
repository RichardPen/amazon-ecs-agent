// +build sudo

// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package engine

import (
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLocalHostVolumeMount(t *testing.T) {
	cfg := defaultTestConfigIntegTest()
	taskEngine, done, _ := setup(cfg, nil, t)
	defer done()

	// creates a task with local volume
	testTask := createTestLocalVolumeMountTask("TestLocalHostVolumeMount")
	stateChangeEvents := taskEngine.StateChangeEvents()
	go taskEngine.AddTask(testTask)

	verifyContainerRunningStateChange(t, taskEngine)
	verifyTaskIsRunning(stateChangeEvents, testTask)
	verifyContainerStoppedStateChange(t, taskEngine)
	verifyTaskIsStopped(stateChangeEvents, testTask)

	assert.NotNil(t, testTask.Containers[0].GetKnownExitCode(), "No exit code found")
	assert.Equal(t, 0, *testTask.Containers[0].GetKnownExitCode(), "Wrong exit code")

	assert.Contains(t, testTask.Volumes[0].Volume.SourcePath(), "/var/lib/docker")
	data, err := ioutil.ReadFile(filepath.Join(testTask.Volumes[0].Volume.SourcePath(), "hello-from-container"))
	assert.Nil(t, err, "Unexpected error")
	assert.Equal(t, "empty-data-volume", strings.TrimSpace(string(data)), "Incorrect file contents")
}
