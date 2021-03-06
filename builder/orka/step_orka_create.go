package orka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/hashicorp/packer/helper/multistep"
	"github.com/hashicorp/packer/packer"
)

type stepOrkaCreate struct {
	failed        bool
	precopyFailed bool
}

func (s *stepOrkaCreate) createOrkaToken(state multistep.StateBag) (string, error) {
	config := state.Get("config").(*Config)
	user := config.OrkaUser
	password := config.OrkaPassword

	// HTTP Client.
	client := &http.Client{}

	reqData := TokenLoginRequest{user, password}
	reqDataJSON, _ := json.Marshal(reqData)
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/%s", config.OrkaEndpoint, "token"),
		bytes.NewBuffer(reqDataJSON),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)

	if err != nil {
		e := fmt.Errorf("Error while logging into the Orka API: %s", err)
		return "", e
	}

	var respData TokenLoginResponse
	respBodyBytes, _ := ioutil.ReadAll(resp.Body)
	json.Unmarshal(respBodyBytes, &respData)
	resp.Body.Close()

	return respData.Token, nil
}

func (s *stepOrkaCreate) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	config := state.Get("config").(*Config)
	ui := state.Get("ui").(packer.Ui)

	// ############################
	// # ORKA API LOGIN FOR TOKEN #
	// ############################

	ui.Say("Logging into Orka API endpoint")

	token, err := s.createOrkaToken(state)

	if err != nil {
		ui.Error(fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err).Error())
		state.Put("error", err)
		s.failed = true
		return multistep.ActionHalt
	}

	ui.Say("Logged in with token")

	// Store the token in the data bag for cleanup later.
	// I am not sure how long these tokens actually last in Orka by default, but I would
	// assume as the build doesn't take hours and hours, it should still be valid by then.
	state.Put("token", token)

	// HTTP Client.
	client := &http.Client{}

	// Builder VM launch image is always the source image. If pre-copy is enabled,
	// however, it will get replaced with the pre-copied destination image instead
	// (below)

	actualImage := config.SourceImage

	if config.ImagePrecopy {
		if config.NoCreateImage {
			ui.Say("Skipping source image pre-copy because of 'no_create_image' being set")
		} else {
			ui.Say(fmt.Sprintf("Pre-copying source image [%s] to destination image [%s]", config.SourceImage, config.ImageName))
			ui.Say("This can take awhile depending on how big the source image is - please wait...")

			imageCopyRequestData := ImageCopyRequest{config.SourceImage, config.ImageName}
			imageCopyRequestDataJSON, _ := json.Marshal(imageCopyRequestData)
			imageCopyRequest, err := http.NewRequest(
				http.MethodPost,
				fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/image/copy"),
				bytes.NewBuffer(imageCopyRequestDataJSON),
			)
			imageCopyRequest.Header.Set("Content-Type", "application/json")
			imageCopyRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			imageCopyResponse, err := client.Do(imageCopyRequest)

			if err != nil {
				ui.Error(fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err).Error())
				state.Put("error", err)
				s.failed = true
				s.precopyFailed = true
				return multistep.ActionHalt
			}

			var imageCopyResponseData ImageCopyResponse
			imageCopyResponseBytes, _ := ioutil.ReadAll(imageCopyResponse.Body)
			json.Unmarshal(imageCopyResponseBytes, &imageCopyResponseData)
			imageCopyResponse.Body.Close()

			if imageCopyResponse.StatusCode != 200 {
				e := fmt.Errorf("Error from API: %s", imageCopyResponse.Status)
				ui.Error(e.Error())
				state.Put("error", e)
				s.failed = true
				return multistep.ActionHalt
			}

			ui.Say("Image copied")
			ui.Say(fmt.Sprintf("Builder VM configuration will use pre-copied base image %s",
				actualImage))

			// Use the destination image (pre-copied) as the actual image to launch
			// the builder VM with.

			actualImage = config.ImageName
		}
	} else {
		ui.Say(fmt.Sprintf("Builder VM configuration will use base image [%s]", actualImage))
	}

	// #######################################
	// # CREATE THE BUILDER VM CONFIGURATION #
	// #######################################

	// Create the builder VM from a pre-existing base-image (required).

	ui.Say(fmt.Sprintf("Creating a Builder VM configuration [%s]",
		config.OrkaVMBuilderName))
	vmCreateConfigRequestData := VMCreateRequest{
		OrkaVMName:  config.OrkaVMBuilderName,
		OrkaVMImage: actualImage,
		OrkaImage:   config.OrkaVMBuilderName,
		OrkaCPUCore: config.OrkaVMCPUCore,
		VCPUCount:   config.OrkaVMCPUCore,
	}
	vmCreateConfigRequestDataJSON, _ := json.Marshal(vmCreateConfigRequestData)
	vmCreateConfigRequest, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/vm/create"),
		bytes.NewBuffer(vmCreateConfigRequestDataJSON),
	)
	vmCreateConfigRequest.Header.Set("Content-Type", "application/json")
	vmCreateConfigRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	vmCreateConfigResponse, err := client.Do(vmCreateConfigRequest)

	if err != nil {
		ui.Error(fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err).Error())
		return multistep.ActionHalt
	}

	var vmCreateConfigResponseData VMCreateResponse
	vmCreateConfigResponseBytes, _ := ioutil.ReadAll(vmCreateConfigResponse.Body)
	json.Unmarshal(vmCreateConfigResponseBytes, &vmCreateConfigResponseData)
	vmCreateConfigResponse.Body.Close()

	if vmCreateConfigResponse.StatusCode != 201 {
		e := fmt.Errorf("%s [%s]", OrkaAPIResponseErrorMessage, vmCreateConfigResponse.Status)
		ui.Error(e.Error())
		state.Put("error", e)
		s.failed = true
		return multistep.ActionHalt
	}

	ui.Say(fmt.Sprintf("Created builder VM configuration [%s]", config.OrkaVMBuilderName))

	// #################
	// # DEPLOY THE VM #
	// #################

	// If that succeeds, let's create a VM based on it, in order to build/pack.

	ui.Say(fmt.Sprintf("Creating builder VM based on [%s] configuration", config.OrkaVMBuilderName))

	vmDeployRequestData := VMDeployRequest{config.OrkaVMBuilderName}
	vmDeployRequestDataJSON, _ := json.Marshal(vmDeployRequestData)
	vmDeployRequest, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/vm/deploy"),
		bytes.NewBuffer(vmDeployRequestDataJSON),
	)
	vmDeployRequest.Header.Set("Content-Type", "application/json")
	vmDeployRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	vmDeployResponse, err := client.Do(vmDeployRequest)
	var vmDeployResponseData VMDeployResponse
	vmDeployResponseBodyBytes, _ := ioutil.ReadAll(vmDeployResponse.Body)
	json.Unmarshal(vmDeployResponseBodyBytes, &vmDeployResponseData)
	vmDeployResponse.Body.Close()

	if vmDeployResponse.StatusCode != 200 {
		state.Put(
			"error",
			fmt.Errorf("Error from API while deploying Orka VM: %s",
				vmDeployResponse.Status))
		s.failed = true
		return multistep.ActionHalt
	}

	// #########################
	// # STORE VM ID AND STATE #
	// #########################

	// Write the VM ID to our state databag for cleanup later.

	state.Put("vmid", vmDeployResponseData.VMId)

	ui.Say(fmt.Sprintf("Created VM [%s]", vmDeployResponseData.VMId))
	ui.Say(fmt.Sprintf("SSH server will be available at [%s:%s]",
		vmDeployResponseData.IP, vmDeployResponseData.SSHPort))

	// Write to our state databag for pick-up by the ssh communicator.

	sshPort, _ := strconv.Atoi(vmDeployResponseData.SSHPort)

	state.Put("ssh_port", sshPort)
	state.Put("ssh_host", vmDeployResponseData.IP)

	// Continue processing
	return multistep.ActionContinue
}

func (s *stepOrkaCreate) precopyImageDelete(state multistep.StateBag) error {
	config := state.Get("config").(*Config)
	ui := state.Get("ui").(packer.Ui)
	token := state.Get("token").(string)

	client := &http.Client{}

	imageDeleteRequestData := ImageDeleteRequest{config.OrkaVMBuilderName}
	imageDeleteRequestDataJSON, _ := json.Marshal(imageDeleteRequestData)
	imageDeleteRequest, err := http.NewRequest(
		http.MethodDelete,
		fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/image/delete"),
		bytes.NewBuffer(imageDeleteRequestDataJSON),
	)
	imageDeleteRequest.Header.Set("Content-Type", "application/json")
	imageDeleteRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	imageDeleteResponse, err := client.Do(imageDeleteRequest)

	if err != nil {
		e := fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err)
		ui.Error(e.Error())
		state.Put("error", err)
		return e
	}

	if imageDeleteResponse.StatusCode != 200 {
		e := fmt.Errorf("Image could not be deleted [%s]", imageDeleteResponse.Status)
		ui.Error(e.Error())
		return e
	}

	ui.Say(fmt.Sprintf("Image deleted [%s]", imageDeleteResponse.Status))
	imageDeleteResponse.Body.Close()

	return nil
}

func (s *stepOrkaCreate) Cleanup(state multistep.StateBag) {
	config := state.Get("config").(*Config)
	ui := state.Get("ui").(packer.Ui)
	token := state.Get("token").(string)

	if config.NoDeleteVM {
		ui.Say("We are skipping the deletion of the builder VM and its configuration because of do_not_delete being set.")

		if config.ImagePrecopy {
			ui.Say(fmt.Sprintf("Pre-copy was performed: image %s will be left and not removed",
				config.ImageName))
		}

		return
	}

	if s.failed {
		if config.ImagePrecopy {
			ui.Say(fmt.Sprintf("Pre-copy was performed: cleaning up pre-copied image %s", config.ImageName))
			precopyDeleteFailed := s.precopyImageDelete(state)

			if precopyDeleteFailed != nil {
				return
			}
		}

		ui.Say("Nothing to cleanup: the builder VM creation, deployment and/or provisioning failed.")
		return
	}

	// vmid := state.Get("vmid").(string)

	ui.Say("Removing builder VM and its configuration...")

	client := &http.Client{}
	vmPurgeRequestData := VMPurgeRequest{config.OrkaVMBuilderName}
	vmPurgeRequestDatJSON, _ := json.Marshal(vmPurgeRequestData)
	vmPurgeRequest, err := http.NewRequest(
		http.MethodDelete,
		fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/vm/purge"),
		bytes.NewBuffer(vmPurgeRequestDatJSON),
	)
	vmPurgeRequest.Header.Set("Content-Type", "application/json")
	vmPurgeRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	vmPurgeResponse, err := client.Do(vmPurgeRequest)

	if err != nil {
		e := fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err)
		ui.Error(e.Error())
		state.Put("error", err)
	}

	if vmPurgeResponse.StatusCode != 200 {
		ui.Error(fmt.Errorf("%s [%s]", OrkaAPIResponseErrorMessage, vmPurgeResponse.Status).Error())
	} else {
		ui.Say("Builder VM and configuration purged")
	}
}
