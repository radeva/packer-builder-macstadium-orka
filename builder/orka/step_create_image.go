package orka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/hashicorp/packer/helper/multistep"
	"github.com/hashicorp/packer/packer"
)

type stepCreateImage struct {
	failed bool
}

func (s *stepCreateImage) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	config := state.Get("config").(*Config)
	ui := state.Get("ui").(packer.Ui)
	vmid := state.Get("vmid").(string)
	token := state.Get("token").(string)

	if config.NoCreateImage {
		ui.Say("Skipping image creation because of 'no_create_image' being set.")
		return multistep.ActionContinue
	}

	// HTTP Client.

	client := &http.Client{
		Timeout: time.Minute * 5,
	}

	ui.Say(fmt.Sprintf("Image creation is using VM ID [%s]", vmid))
	ui.Say(fmt.Sprintf("Image name is [%s]", config.ImageName))

	// ui.Say("We must stop and then start (restart) the VM first")

	// stopVMRequestData := VMStopRequest{vmid}
	// stopVMRequestDataJSON, _ := json.Marshal(stopVMRequestData)
	// vmStopRequest, err := http.NewRequest(
	// 	http.MethodPost,
	// 	fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/vm/stop"),
	// 	bytes.NewBuffer(stopVMRequestDataJSON),
	// )
	// vmStopRequest.Header.Set("Content-Type", "application/json")
	// vmStopRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	// ui.Say("Stopping and waiting 10 seconds...")
	// vmStopResponse, err := client.Do(vmStopRequest)
	// if err != nil {
	// 	state.Put("error", err)
	// 	return multistep.ActionHalt
	// }
	// var vmStopResponseData VMStopResponse
	// vmStopRespBytes, _ := ioutil.ReadAll(vmStopResponse.Body)
	// json.Unmarshal(vmStopRespBytes, &vmStopResponseData)
	// vmStopResponse.Body.Close()
	// time.Sleep(time.Second * 10)

	// startVMRequestData := VMStartRequest{vmid}
	// startVMRequestDataJSON, _ := json.Marshal(startVMRequestData)
	// vmStartRequest, err := http.NewRequest(
	// 	http.MethodPost,
	// 	fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/vm/start"),
	// 	bytes.NewBuffer(startVMRequestDataJSON),
	// )
	// vmStartRequest.Header.Set("Content-Type", "application/json")
	// vmStartRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	// ui.Say("Starting and waiting 30 seconds...")
	// vmStartResponse, err := client.Do(vmStartRequest)
	// if err != nil {
	// 	ui.Error(fmt.Errorf("Error while starting VM %s: %s", vmid, err).Error())
	// 	return multistep.ActionHalt
	// }
	// var vmStartResponseData VMStartResponse
	// vmStartResponseBytes, _ := ioutil.ReadAll(vmStartResponse.Body)
	// json.Unmarshal(vmStartResponseBytes, &vmStartResponseData)
	// vmStartRequest.Body.Close()
	// time.Sleep(time.Second * 30)

	// Now that the VM is stopped, we can commit or save it.

	ui.Say("Please wait as this can take a little while...")

	if config.ImagePrecopy {
		// If we are using the pre-copy logic, then we just re-commit the image back.

		ui.Say("Committing existing image since pre-copy is being used")

		imageCommitRequestData := ImageCommitRequest{vmid}
		imageCommitRequestDataJSON, _ := json.Marshal(imageCommitRequestData)
		imageCommitRequest, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/image/commit"),
			bytes.NewBuffer(imageCommitRequestDataJSON),
		)
		imageCommitRequest.Header.Set("Content-Type", "application/json")
		imageCommitRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		imageCommitResponse, err := client.Do(imageCommitRequest)

		if err != nil {
			e := fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err)
			ui.Error(e.Error())
			return multistep.ActionHalt
		}

		var imageCommitResponseData ImageCommitResponse
		imageCommitResponseBytes, _ := ioutil.ReadAll(imageCommitResponse.Body)
		json.Unmarshal(imageCommitResponseBytes, &imageCommitResponseData)
		imageCommitResponse.Body.Close()

		if imageCommitResponse.StatusCode != 200 {
			ui.Error(fmt.Errorf("Error committing image [%s]", imageCommitResponse.Status).Error())
		} else {
			ui.Say(fmt.Sprintf("Image comitted [%s] [%s]", imageCommitResponse.Status, imageCommitResponseData.Message))
		}
	} else {
		// By default we use the save endpoint to generate a new base image from
		// the running VM's current image.

		ui.Say(fmt.Sprintf("Saving new image [%s]", config.ImageName))

		imageSaveRequestData := ImageSaveRequest{vmid, config.ImageName}
		imageSaveRequestDataJSON, _ := json.Marshal(imageSaveRequestData)
		imageSaveRequest, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("%s/%s", config.OrkaEndpoint, "resources/image/save"),
			bytes.NewBuffer(imageSaveRequestDataJSON),
		)
		imageSaveRequest.Header.Set("Content-Type", "application/json")
		imageSaveRequest.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		imageSaveResponse, err := client.Do(imageSaveRequest)

		if err != nil {
			ui.Error(fmt.Errorf("%s [%s]", OrkaAPIRequestErrorMessage, err).Error())
			return multistep.ActionHalt
		}

		var imageSaveResponseData ImageSaveResponse
		imageSaveResponseBytes, _ := ioutil.ReadAll(imageSaveResponse.Body)
		json.Unmarshal(imageSaveResponseBytes, &imageSaveResponseData)
		imageSaveResponse.Body.Close()

		if imageSaveResponse.StatusCode != 200 {
			ui.Error(fmt.Errorf("%s [%s]", OrkaAPIResponseErrorMessage, imageSaveResponse.Status).Error())
			return multistep.ActionHalt
		}

		ui.Say(fmt.Sprintf("Image saved [%s] [%s]", imageSaveResponse.Status, imageSaveResponseData.Message))
	}

	return multistep.ActionContinue
}

func (s *stepCreateImage) Cleanup(state multistep.StateBag) {
	ui := state.Get("ui").(packer.Ui)
	vmid := state.Get("vmid").(string)

	if vmid == "" {
		return
	}

	_, cancelled := state.GetOk(multistep.StateCancelled)
	_, halted := state.GetOk(multistep.StateHalted)

	if !cancelled && !halted {
		return
	}

	ui.Say("Image cleanup complete.")
}
