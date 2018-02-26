// +build integration,windows

// Copyright 2014-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
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
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/api"
	"github.com/aws/amazon-ecs-agent/agent/utils"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRegistryHost      = "127.0.0.1:51670"
	testBusyboxImage      = testRegistryHost + "/busybox:latest"
	testRegistryImage     = "amazon/amazon-ecs-netkitten:make"
	testAuthRegistryHost  = "127.0.0.1:51671"
	testAuthRegistryImage = "127.0.0.1:51671/amazon/amazon-ecs-netkitten:latest"
	testAuthUser          = "user"
	testAuthPass          = "swordfish"
)

// TODO Modify the container ip to localhost after the AMI has the required feature
// https://github.com/docker/for-win/issues/204#issuecomment-352899657

var endpoint = utils.DefaultIfBlank(os.Getenv(DockerEndpointEnvVariable), dockerEndpoint)

func removeImage(img string) {
	removeEndpoint := utils.DefaultIfBlank(os.Getenv(DockerEndpointEnvVariable), dockerEndpoint)
	client, _ := docker.NewClient(removeEndpoint)

	client.RemoveImage(img)
}

func dialWithRetries(proto string, address string, tries int, timeout time.Duration) (net.Conn, error) {
	var err error
	var conn net.Conn
	for i := 0; i < tries; i++ {
		conn, err = net.DialTimeout(proto, address, timeout)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return conn, err
}

func getContainerIP(client *docker.Client, id string) (string, error) {
	dockerContainer, err := client.InspectContainer(id)
	if err != nil {
		return "", err
	}

	networks := dockerContainer.NetworkSettings.Networks
	if len(networks) != 1 {
		return "", fmt.Errorf("getContainerIP: inspect return multiple networks of container")
	}
	for _, v := range networks {
		return v.IPAddress, nil
	}
	return "", nil
}

// TestStartStopUnpulledImage ensures that an unpulled image is successfully
// pulled, run, and stopped via docker.
func TestStartStopUnpulledImage(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()
	// Ensure this image isn't pulled by deleting it
	removeImage(testRegistryImage)

	testTask := createTestTask("testStartUnpulled")

	stateChangeEvents := taskEngine.StateChangeEvents()

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")

}

// TestStartStopUnpulledImageDigest ensures that an unpulled image with
// specified digest is successfully pulled, run, and stopped via docker.
func TestStartStopUnpulledImageDigest(t *testing.T) {
	imageDigest := "cggruszka/microsoft-windows-helloworld@sha256:89282ba3e122e461381eae854d142c6c4895fcc1087d4849dbe4786fb21018f8"
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()
	// Ensure this image isn't pulled by deleting it
	removeImage(imageDigest)

	testTask := createTestTask("testStartUnpulledDigest")
	testTask.Containers[0].Image = imageDigest

	stateChangeEvents := taskEngine.StateChangeEvents()

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")
}

// TestPortForward runs a container serving data on the randomly chosen port
// 24751 and verifies that when you do forward the port you can access it and if
// you don't forward the port you can't
func TestPortForward(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()
	client, _ := docker.NewClient(endpoint)

	testArn := "testPortForwardFail"
	testTask := createTestTask(testArn)
	testTask.Containers[0].Image = testRegistryImage
	testTask.Containers[0].Command = []string{"-l=24751", "-serve", "ecs test container"}

	// Port not forwarded; verify we can't access it
	go taskEngine.AddTask(testTask)

	err := verifyTaskIsRunning(stateChangeEvents, testTask)
	require.NoError(t, err)
	containerMap, _ := taskEngine.(*DockerTaskEngine).state.ContainerMapByArn(testTask.Arn)
	cid := containerMap[testTask.Containers[0].Name].DockerID

	cip, err := getContainerIP(client, cid)
	require.NoError(t, err, "failed to acquire container ip from docker")

	time.Sleep(50 * time.Millisecond) // wait for Docker
	_, err = net.DialTimeout("tcp", fmt.Sprintf("%s:24751", cip), 200*time.Millisecond)
	if err == nil {
		t.Error("Did not expect to be able to dial 127.0.0.1:24751 but didn't get error")
	}

	// Kill the existing container now to make the test run more quickly.
	err = client.KillContainer(docker.KillContainerOptions{ID: cid})
	if err != nil {
		t.Error("Could not kill container", err)
	}

	verifyTaskIsStopped(stateChangeEvents, testTask)

	// Now forward it and make sure that works
	testArn = "testPortForwardWorking"
	testTask = createTestTask(testArn)
	testTask.Containers[0].Image = testRegistryImage
	testTask.Containers[0].Command = []string{"-l=24751", "-serve", "ecs test container"}
	testTask.Containers[0].Ports = []api.PortBinding{{ContainerPort: 24751, HostPort: 24751}}

	taskEngine.AddTask(testTask)

	err = verifyTaskIsRunning(stateChangeEvents, testTask)
	require.NoError(t, err)

	containerMap, _ = taskEngine.(*DockerTaskEngine).state.ContainerMapByArn(testTask.Arn)
	cid = containerMap[testTask.Containers[0].Name].DockerID
	cip, err = getContainerIP(client, cid)
	require.NoError(t, err, "failed to acquire container ip from docker")

	time.Sleep(50 * time.Millisecond) // wait for Docker
	conn, err := dialWithRetries("tcp", fmt.Sprintf("%s:24751", cip), 10, 20*time.Millisecond)
	if err != nil {
		t.Fatal("Error dialing simple container " + err.Error())
	}

	var response []byte
	for i := 0; i < 10; i++ {
		response, err = ioutil.ReadAll(conn)
		if err != nil {
			t.Error("Error reading response", err)
		}
		if len(response) > 0 {
			break
		}
		// Retry for a non-blank response. The container in docker 1.7+ sometimes
		// isn't up quickly enough and we get a blank response. It's still unclear
		// to me if this is a docker bug or netkitten bug
		t.Log("Retrying getting response from container; got nothing")
		time.Sleep(100 * time.Millisecond)
	}
	if string(response) != "ecs test container" {
		t.Error("Got response: " + string(response) + " instead of 'ecs test container'")
	}

	// Stop the existing container now
	taskUpdate := *testTask
	taskUpdate.SetDesiredStatus(api.TaskStopped)
	go taskEngine.AddTask(&taskUpdate)
	verifyTaskIsStopped(stateChangeEvents, testTask)
}

// TestMultiplePortForwards tests that two ldockinks containers in the same task can
// both expose ports successfully
func TestMultiplePortForwards(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()
	client, _ := docker.NewClient(endpoint)

	// Forward it and make sure that works
	testArn := "testMultiplePortForwards"
	testTask := createTestTask(testArn)
	testTask.Containers[0].Image = testRegistryImage
	testTask.Containers[0].Command = []string{"-l=24751", "-serve", "ecs test container1"}
	testTask.Containers[0].Ports = []api.PortBinding{{ContainerPort: 24751, HostPort: 24751}}
	testTask.Containers[0].Essential = false
	testTask.Containers = append(testTask.Containers, createTestContainer())
	testTask.Containers[1].Name = "nc2"
	testTask.Containers[1].Image = testRegistryImage
	testTask.Containers[1].Command = []string{"-l=24751", "-serve", "ecs test container2"}
	testTask.Containers[1].Ports = []api.PortBinding{{ContainerPort: 24751, HostPort: 24752}}

	go taskEngine.AddTask(testTask)

	err := verifyTaskIsRunning(stateChangeEvents, testTask)
	require.NoError(t, err)

	containerMap, _ := taskEngine.(*DockerTaskEngine).state.ContainerMapByArn(testTask.Arn)
	cid1 := containerMap[testTask.Containers[0].Name].DockerID
	cid2 := containerMap[testTask.Containers[1].Name].DockerID

	cip1, err := getContainerIP(client, cid1)
	require.NoError(t, err, "failed to acquire the container ip from docker")
	cip2, err := getContainerIP(client, cid2)
	require.NoError(t, err, "failed to acquire the container ip from docker")

	time.Sleep(50 * time.Millisecond) // wait for Docker
	conn, err := dialWithRetries("tcp", fmt.Sprintf("%s:24751", cip1), 10, 20*time.Millisecond)
	if err != nil {
		t.Fatal("Error dialing simple container 1 " + err.Error())
	}
	t.Log("Dialed first container")
	response, _ := ioutil.ReadAll(conn)
	if string(response) != "ecs test container1" {
		t.Error("Got response: " + string(response) + " instead of 'ecs test container1'")
	}
	t.Log("Read first container")
	conn, err = dialWithRetries("tcp", fmt.Sprintf("%s:24751", cip2), 10, 20*time.Millisecond)
	if err != nil {
		t.Fatal("Error dialing simple container 2 " + err.Error())
	}
	t.Log("Dialed second container")
	response, _ = ioutil.ReadAll(conn)
	if string(response) != "ecs test container2" {
		t.Error("Got response: " + string(response) + " instead of 'ecs test container2'")
	}
	t.Log("Read second container")

	taskUpdate := *testTask
	taskUpdate.SetDesiredStatus(api.TaskStopped)
	go taskEngine.AddTask(&taskUpdate)
	verifyTaskIsStopped(stateChangeEvents, testTask)
}

// TestDynamicPortForward runs a container serving data on a port chosen by the
// docker deamon and verifies that the port is reported in the state-change
func TestDynamicPortForward(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	testArn := "testDynamicPortForward"
	testTask := createTestTask(testArn)
	testTask.Containers[0].Image = testRegistryImage
	testTask.Containers[0].Command = []string{"-l=24751", "-serve", "ecs test container"}
	// No HostPort = docker should pick
	testTask.Containers[0].Ports = []api.PortBinding{{ContainerPort: 24751}}

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	portBindings := event.(api.ContainerStateChange).PortBindings

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	if len(portBindings) != 1 {
		t.Error("PortBindings was not set; should have been len 1", portBindings)
	}
	var bindingFor24751 uint16
	for _, binding := range portBindings {
		if binding.ContainerPort == 24751 {
			bindingFor24751 = binding.HostPort
		}
	}
	if bindingFor24751 == 0 {
		t.Error("Could not find the port mapping for 24751!")
	}

	client, _ := docker.NewClient(endpoint)
	containerMap, _ := taskEngine.(*DockerTaskEngine).state.ContainerMapByArn(testTask.Arn)
	cid := containerMap[testTask.Containers[0].Name].DockerID
	cip, err := getContainerIP(client, cid)
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond) // wait for Docker
	//See TODO above: conn, err := dialWithRetries("tcp", cip+":"+strconv.Itoa(int(bindingFor24751)), 10, 20*time.Millisecond)
	conn, err := dialWithRetries("tcp", cip+":24751", 10, 20*time.Millisecond)
	if err != nil {
		t.Fatal("Error dialing simple container " + err.Error())
	}

	response, _ := ioutil.ReadAll(conn)
	if string(response) != "ecs test container" {
		t.Error("Got response: " + string(response) + " instead of 'ecs test container'")
	}

	// Kill the existing container now
	taskUpdate := *testTask
	taskUpdate.SetDesiredStatus(api.TaskStopped)
	go taskEngine.AddTask(&taskUpdate)

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")
}

func TestMultipleDynamicPortForward(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	testArn := "testDynamicPortForward2"
	testTask := createTestTask(testArn)
	testTask.Containers[0].Image = testRegistryImage
	testTask.Containers[0].Command = []string{"-l=24751", "-serve", "ecs test container", `-loop`}
	// No HostPort or 0 hostport; docker should pick two ports for us
	testTask.Containers[0].Ports = []api.PortBinding{{ContainerPort: 24751}, {ContainerPort: 24751, HostPort: 0}}

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	portBindings := event.(api.ContainerStateChange).PortBindings

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	if len(portBindings) != 2 {
		t.Error("Could not bind to two ports from one container port", portBindings)
	}
	var bindingFor24751_1 uint16
	var bindingFor24751_2 uint16
	for _, binding := range portBindings {
		if binding.ContainerPort == 24751 {
			if bindingFor24751_1 == 0 {
				bindingFor24751_1 = binding.HostPort
			} else {
				bindingFor24751_2 = binding.HostPort
			}
		}
	}
	if bindingFor24751_1 == 0 {
		t.Error("Could not find the port mapping for 24751!")
	}
	if bindingFor24751_2 == 0 {
		t.Error("Could not find the port mapping for 24751!")
	}

	client, _ := docker.NewClient(endpoint)
	containerMap, _ := taskEngine.(*DockerTaskEngine).state.ContainerMapByArn(testTask.Arn)
	cid := containerMap[testTask.Containers[0].Name].DockerID
	cip, err := getContainerIP(client, cid)
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond) // wait for Docker
	conn, err := dialWithRetries("tcp", fmt.Sprintf("%s:24751", cip), 10, 20*time.Millisecond)
	if err != nil {
		t.Fatal("Error dialing simple container " + err.Error())
	}

	response, _ := ioutil.ReadAll(conn)
	if string(response) != "ecs test container" {
		t.Error("Got response: " + string(response) + " instead of 'ecs test container'")
	}
	// See TODO above
	/*
		conn, err = dialWithRetries("tcp", "127.0.0.1:"+strconv.Itoa(int(bindingFor24751_2)), 10, 20*time.Millisecond)
		if err != nil {
			t.Fatal("Error dialing simple container " + err.Error())
		}

		response, _ = ioutil.ReadAll(conn)
		if string(response) != "ecs test container" {
			t.Error("Got response: " + string(response) + " instead of 'ecs test container'")
		}*/

	// Kill the existing container now
	taskUpdate := *testTask
	taskUpdate.SetDesiredStatus(api.TaskStopped)
	go taskEngine.AddTask(&taskUpdate)

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")
}

func TestVolumesFrom(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	testTask := createTestTask("testVolumeContainer")
	testTask.Containers[0].Image = testVolumeImage
	testTask.Containers = append(testTask.Containers, createTestContainer())
	testTask.Containers[1].Name = "test2"
	testTask.Containers[1].Image = testVolumeImage
	testTask.Containers[1].VolumesFrom = []api.VolumeFrom{{SourceContainer: testTask.Containers[0].Name}}
	testTask.Containers[1].Command = []string{"-c", "$output= (cat /data/test-file); if ($output -eq test) { Exit 42 } else { Exit 1 }"}

	go taskEngine.AddTask(testTask)

	err := verifyTaskIsRunning(stateChangeEvents, testTask)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, *testTask.Containers[1].GetKnownExitCode(), 42)

	taskUpdate := *testTask
	taskUpdate.SetDesiredStatus(api.TaskStopped)
	go taskEngine.AddTask(&taskUpdate)

	verifyTaskIsStopped(stateChangeEvents, testTask)
}

func TestVolumesFromRO(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	testTask := createTestTask("testVolumeROContainer")
	testTask.Containers[0].Image = testVolumeImage
	for i := 0; i < 3; i++ {
		cont := createTestContainer()
		cont.Name = "test" + strconv.Itoa(i)
		cont.Image = testVolumeImage
		cont.Essential = i > 0
		testTask.Containers = append(testTask.Containers, cont)
	}
	testTask.Containers[1].VolumesFrom = []api.VolumeFrom{{SourceContainer: testTask.Containers[0].Name, ReadOnly: true}}
	testTask.Containers[1].Command = []string{"New-Item c:/volume/readonly-fs; if ($?) { Exit 0 } else { Exit 42 }"}
	testTask.Containers[2].VolumesFrom = []api.VolumeFrom{{SourceContainer: testTask.Containers[0].Name}}
	testTask.Containers[2].Command = []string{"New-Item c:/volume/readonly-fs-2; if ($?) { Exit 0 } else { Exit 42 }"}
	testTask.Containers[3].VolumesFrom = []api.VolumeFrom{{SourceContainer: testTask.Containers[0].Name, ReadOnly: false}}
	testTask.Containers[3].Command = []string{"New-Item c:/volume/readonly-fs-3; if ($?) { Exit 0 } else { Exit 42 }"}

	go taskEngine.AddTask(testTask)

	verifyTaskIsStopped(stateChangeEvents, testTask)

	if testTask.Containers[1].GetKnownExitCode() == nil || *testTask.Containers[1].GetKnownExitCode() != 42 {
		t.Error("Didn't exit due to failure to touch ro fs as expected: ", *testTask.Containers[1].GetKnownExitCode())
	}
	if testTask.Containers[2].GetKnownExitCode() == nil || *testTask.Containers[2].GetKnownExitCode() != 0 {
		t.Error("Couldn't touch with default of rw")
	}
	if testTask.Containers[3].GetKnownExitCode() == nil || *testTask.Containers[3].GetKnownExitCode() != 0 {
		t.Error("Couldn't touch with explicit rw")
	}
}
