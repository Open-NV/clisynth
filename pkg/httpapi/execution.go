package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/open-nv/clisynth/pkg/engine"
)

const (
	maxExecutionDevices  = 10_000
	maxExecutionCommands = 100
)

var supportedPlatforms = map[string]struct{}{
	"cisco_ios":     {},
	"cisco_iosxe":   {},
	"cisco_nxos":    {},
	"cisco_iosxr":   {},
	"arista_eos":    {},
	"juniper_junos": {},
	"f5_tmos":       {},
	"citrix_adc":    {},
}

type ExecutionRequest struct {
	APIVersion        string            `json:"apiVersion"`
	RequestID         string            `json:"requestId"`
	RunID             string            `json:"runId,omitempty"`
	Mode              string            `json:"mode"`
	InventoryRevision string            `json:"inventoryRevision,omitempty"`
	PackRevision      string            `json:"packRevision,omitempty"`
	Devices           []ExecutionDevice `json:"devices"`
	Commands          []string          `json:"commands"`
	Concurrency       *int              `json:"concurrency,omitempty"`
	TimeoutSeconds    *int              `json:"timeoutSeconds,omitempty"`
	CredentialRef     string            `json:"credentialRef,omitempty"`
}

type ExecutionDevice struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Platform          string           `json:"platform"`
	ManagementAddress string           `json:"managementAddress"`
	Role              string           `json:"role"`
	Site              string           `json:"site"`
	Status            string           `json:"status"`
	OSVersion         *string          `json:"osVersion,omitempty"`
	ImageFile         *string          `json:"imageFile,omitempty"`
	Vendor            *string          `json:"vendor,omitempty"`
	Model             *string          `json:"model,omitempty"`
	Serial            *string          `json:"serial,omitempty"`
	Tags              []string         `json:"tags,omitempty"`
	CustomFields      map[string]any   `json:"customFields,omitempty"`
	Source            *ExecutionSource `json:"source,omitempty"`
}

type ExecutionSource struct {
	Kind     string  `json:"kind"`
	RecordID string  `json:"recordId"`
	Revision *string `json:"revision,omitempty"`
}

type ExecutionResponse struct {
	RequestID string                `json:"requestId"`
	Results   []ExecutionTaskResult `json:"results"`
}

type ExecutionTaskResult struct {
	RunID      string                `json:"runId"`
	TaskID     string                `json:"taskId"`
	DeviceID   string                `json:"deviceId"`
	DeviceName string                `json:"deviceName"`
	Platform   string                `json:"platform"`
	Command    string                `json:"command"`
	State      string                `json:"state"`
	Output     *string               `json:"output,omitempty"`
	Parsed     map[string]any        `json:"parsed,omitempty"`
	Error      *ExecutionResultError `json:"error,omitempty"`
	StartedAt  time.Time             `json:"startedAt"`
	FinishedAt time.Time             `json:"finishedAt"`
	Attempt    int                   `json:"attempt"`
	Backend    string                `json:"backend"`
	Synthetic  bool                  `json:"synthetic"`
}

type ExecutionResultError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (a *API) executeBatch(writer http.ResponseWriter, request *http.Request) {
	var payload ExecutionRequest
	if err := a.decodeJSON(writer, request, &payload); err != nil {
		writeAPIError(writer, 400, engine.CodeInvalidRequest, err.Error())
		return
	}
	if err := payload.validate(); err != nil {
		writeAPIError(writer, 422, engine.CodeInvalidRequest, err.Error())
		return
	}

	runID := payload.RunID
	if runID == "" {
		runID = payload.RequestID
	}
	started := time.Now().UTC()
	ctx := request.Context()
	if payload.TimeoutSeconds != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*payload.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	seed := executionSeed(runID, payload.RequestID, payload.InventoryRevision, payload.PackRevision)
	renderRequests := make([]engine.RenderRequest, 0, len(payload.Devices)*len(payload.Commands))
	for _, device := range payload.Devices {
		document := device.document()
		for _, command := range payload.Commands {
			renderRequests = append(renderRequests, engine.RenderRequest{
				Platform: device.Platform,
				Command:  command,
				Device:   document,
				Seed:     seed,
			})
		}
	}
	concurrency := 0
	if payload.Concurrency != nil {
		concurrency = *payload.Concurrency
	}
	batch, err := a.engine.RenderBatch(ctx, engine.BatchRequest{Requests: renderRequests, Concurrency: concurrency})
	if err != nil {
		writeEngineError(writer, err)
		return
	}
	finished := time.Now().UTC()
	results := make([]ExecutionTaskResult, len(batch.Results))
	for index, item := range batch.Results {
		deviceIndex := index / len(payload.Commands)
		commandIndex := index % len(payload.Commands)
		device := payload.Devices[deviceIndex]
		command := payload.Commands[commandIndex]
		result := ExecutionTaskResult{
			RunID:      runID,
			TaskID:     executionTaskID(runID, device.ID, command),
			DeviceID:   device.ID,
			DeviceName: device.Name,
			Platform:   device.Platform,
			Command:    command,
			State:      "succeeded",
			StartedAt:  started,
			FinishedAt: finished,
			Attempt:    1,
			Backend:    "clisynth",
			Synthetic:  true,
		}
		if item.Error != nil {
			result.State = executionErrorState(item.Error.Code)
			result.Error = &ExecutionResultError{Code: strings.ToUpper(item.Error.Code), Message: item.Error.Message, Retryable: false}
		} else {
			result.Output = &item.Result.Output
			result.Parsed = item.Result.Parsed
		}
		results[index] = result
	}
	writeJSON(writer, 200, ExecutionResponse{RequestID: payload.RequestID, Results: results})
}

func (request ExecutionRequest) validate() error {
	if request.APIVersion != "v1" {
		return errors.New("apiVersion must be v1")
	}
	if len(request.RequestID) < 1 || len(request.RequestID) > 128 {
		return errors.New("requestId must contain 1 to 128 characters")
	}
	if len(request.RunID) > 128 {
		return errors.New("runId must be at most 128 characters")
	}
	if request.Mode != "emulated" {
		return errors.New("CliSynth requires mode=emulated")
	}
	if len(request.InventoryRevision) > 256 || len(request.PackRevision) > 256 {
		return errors.New("inventoryRevision and packRevision must be at most 256 characters")
	}
	if len(request.Devices) < 1 || len(request.Devices) > maxExecutionDevices {
		return fmt.Errorf("devices must contain 1 to %d items", maxExecutionDevices)
	}
	if len(request.Commands) < 1 || len(request.Commands) > maxExecutionCommands {
		return fmt.Errorf("commands must contain 1 to %d items", maxExecutionCommands)
	}
	if request.Concurrency != nil && (*request.Concurrency < 1 || *request.Concurrency > 10_000) {
		return errors.New("concurrency must be between 1 and 10000 when set")
	}
	if request.TimeoutSeconds != nil && (*request.TimeoutSeconds < 1 || *request.TimeoutSeconds > 3600) {
		return errors.New("timeoutSeconds must be between 1 and 3600 when set")
	}
	if len(request.CredentialRef) > 512 {
		return errors.New("credentialRef must be at most 512 characters")
	}
	commands := make(map[string]struct{}, len(request.Commands))
	for index := range request.Commands {
		request.Commands[index] = strings.TrimSpace(request.Commands[index])
		if request.Commands[index] == "" || len(request.Commands[index]) > 512 {
			return fmt.Errorf("command %d must contain 1 to 512 characters", index)
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(request.Commands[index]), " "))
		if _, exists := commands[normalized]; exists {
			return fmt.Errorf("commands must be unique; duplicate %q", request.Commands[index])
		}
		commands[normalized] = struct{}{}
	}
	deviceIDs := make(map[string]struct{}, len(request.Devices))
	for index, device := range request.Devices {
		if err := device.validate(); err != nil {
			return fmt.Errorf("device %d: %w", index, err)
		}
		if _, duplicate := deviceIDs[device.ID]; duplicate {
			return fmt.Errorf("devices must have unique IDs; duplicate %q", device.ID)
		}
		deviceIDs[device.ID] = struct{}{}
	}
	return nil
}

func (device ExecutionDevice) validate() error {
	if device.ID == "" || len(device.ID) > 256 {
		return errors.New("id must contain 1 to 256 characters")
	}
	if device.Name == "" || len(device.Name) > 253 {
		return errors.New("name must contain 1 to 253 characters")
	}
	if _, ok := supportedPlatforms[device.Platform]; !ok {
		return fmt.Errorf("unsupported platform %q", device.Platform)
	}
	if device.ManagementAddress == "" || len(device.ManagementAddress) > 255 {
		return errors.New("managementAddress must contain 1 to 255 characters")
	}
	if device.Role == "" || len(device.Role) > 128 || device.Site == "" || len(device.Site) > 128 || device.Status == "" || len(device.Status) > 64 {
		return errors.New("role, site, and status are required and exceed their allowed length")
	}
	if len(device.Tags) > 128 || len(device.CustomFields) > 256 {
		return errors.New("tags or customFields exceed their item limit")
	}
	seenTags := make(map[string]struct{}, len(device.Tags))
	for _, tag := range device.Tags {
		if len(tag) > 128 {
			return errors.New("tag exceeds 128 characters")
		}
		if _, duplicate := seenTags[tag]; duplicate {
			return fmt.Errorf("duplicate tag %q", tag)
		}
		seenTags[tag] = struct{}{}
	}
	for name, value := range map[string]*string{"osVersion": device.OSVersion, "imageFile": device.ImageFile, "vendor": device.Vendor, "model": device.Model, "serial": device.Serial} {
		if value == nil {
			continue
		}
		limit := 256
		if name == "imageFile" {
			limit = 512
		} else if name == "vendor" {
			limit = 128
		}
		if len(*value) > limit {
			return fmt.Errorf("%s exceeds %d characters", name, limit)
		}
	}
	if device.Source != nil {
		switch device.Source.Kind {
		case "nautobot", "netbox", "file", "demo":
		default:
			return fmt.Errorf("invalid source kind %q", device.Source.Kind)
		}
		if device.Source.RecordID == "" {
			return errors.New("source.recordId is required")
		}
	}
	return nil
}

func (device ExecutionDevice) document() map[string]any {
	document := map[string]any{
		"id":                 device.ID,
		"name":               device.Name,
		"hostname":           device.Name,
		"platform":           device.Platform,
		"managementAddress":  device.ManagementAddress,
		"management_address": device.ManagementAddress,
		"role":               device.Role,
		"site":               device.Site,
		"status":             device.Status,
		"tags":               append([]string(nil), device.Tags...),
		"customFields":       cloneDocument(device.CustomFields),
		"custom_fields":      cloneDocument(device.CustomFields),
	}
	system := map[string]any{"oper_state": operationalState(device.Status)}
	if device.OSVersion != nil {
		document["osVersion"] = *device.OSVersion
		document["software_version"] = *device.OSVersion
		system["os_version"] = *device.OSVersion
	}
	if device.ImageFile != nil {
		document["imageFile"] = *device.ImageFile
		document["image_file"] = *device.ImageFile
		system["image"] = *device.ImageFile
	}
	if device.Model != nil {
		document["model"] = *device.Model
		system["model"] = *device.Model
	}
	if device.Serial != nil {
		document["serial"] = *device.Serial
		system["serial"] = *device.Serial
	}
	if device.Vendor != nil {
		document["vendor"] = *device.Vendor
	}
	document["system"] = system
	for key, value := range device.CustomFields {
		if customMap, ok := value.(map[string]any); ok {
			if existing, exists := document[key].(map[string]any); exists {
				document[key] = mergeDocument(existing, customMap)
				continue
			}
		}
		document[key] = cloneValue(value)
	}
	return document
}

func cloneDocument(source map[string]any) map[string]any {
	return mergeDocument(nil, source)
}

func mergeDocument(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		result[key] = cloneValue(value)
	}
	for key, value := range overlay {
		if next, ok := value.(map[string]any); ok {
			if current, exists := result[key].(map[string]any); exists {
				result[key] = mergeDocument(current, next)
				continue
			}
		}
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneDocument(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	default:
		return typed
	}
}

func operationalState(status string) string {
	switch strings.ToLower(status) {
	case "active", "online", "healthy", "up":
		return "up"
	case "offline", "failed", "down", "decommissioned":
		return "down"
	default:
		return status
	}
}

func executionTaskID(runID, deviceID, command string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + deviceID + "\x00" + command))
	return "task_" + hex.EncodeToString(digest[:12])
}

func executionSeed(parts ...string) int64 {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func executionErrorState(code string) string {
	switch code {
	case engine.CodeDefinitionNotFound:
		return "unsupported"
	case engine.CodeCanceled:
		return "cancelled"
	default:
		return "failed"
	}
}
