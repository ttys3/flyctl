package flaps

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-querystring/query"
	"github.com/samber/lo"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/internal/metrics"
	"github.com/superfly/flyctl/terminal"
)

var NonceHeader = "fly-machine-lease-nonce"

type flapsActionInfo struct {
	action          flapsAction
	machineID       string
	queryParameters url.Values
	headers         map[string][]string
}

func flapsActionInfoToEndpoint(actionInfo flapsActionInfo, appName string) (string, error) {
	var endpoint string

	if callEndpoint, ok := flapsActionToEndpoint[actionInfo.action]; ok {
		if actionInfo.machineID != "" {
			endpoint = fmt.Sprintf("/%s", actionInfo.machineID)
		}
		if callEndpoint != "" {
			endpoint += fmt.Sprintf("/%s", callEndpoint)
		}

		queryParams := actionInfo.queryParameters.Encode()

		endpoint = fmt.Sprintf("%s?%s", endpoint, queryParams)
		endpoint = fmt.Sprintf("/apps/%s/machines%s", appName, endpoint)

	} else {
		return "", fmt.Errorf("flaps action %s (%d) does not exist", &actionInfo.action, actionInfo.action)
	}

	return endpoint, nil
}

func (f *Client) sendRequestMachines(ctx context.Context, actionInfo flapsActionInfo, in, out interface{}) error {
	endpoint, err := flapsActionInfoToEndpoint(actionInfo, f.appName)
	if err != nil {
		return err
	}

	var method string
	var ok bool
	if method, ok = flapsActionToMethod[actionInfo.action]; !ok {
		return fmt.Errorf("method not found for action %s (%d)", actionInfo.action.String(), actionInfo.action)
	}

	startTimer := time.Now()
	statusCode, err := f._sendRequest(ctx, method, endpoint, in, out, actionInfo.headers)
	time := time.Since(startTimer)

	defer func() {
		// If the status code is 0, then we bumped into an error that doesn't involve sending data
		if statusCode != 0 {
			sendFlapsCallMetric(ctx, actionInfo.action, statusCode, time)
		}
	}()

	if err != nil {
		return err
	}

	return err
}

func (f *Client) Launch(ctx context.Context, builder api.LaunchMachineInput) (out *api.Machine, err error) {
	metrics.Started(ctx, "machine_launch")
	sendUpdateMetrics := metrics.StartTiming(ctx, "machine_launch/duration")
	defer func() {
		metrics.Status(ctx, "machine_launch", err == nil)
		if err == nil {
			sendUpdateMetrics()
		}
	}()

	out = new(api.Machine)
	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: launch}, builder, out); err != nil {
		return nil, fmt.Errorf("failed to launch VM: %w", err)
	}

	return out, nil
}

func (f *Client) Update(ctx context.Context, builder api.LaunchMachineInput, nonce string) (out *api.Machine, err error) {
	headers := make(map[string][]string)
	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	metrics.Started(ctx, "machine_update")
	sendUpdateMetrics := metrics.StartTiming(ctx, "machine_update/duration")
	defer func() {
		metrics.Status(ctx, "machine_update", err == nil)
		if err == nil {
			sendUpdateMetrics()
		}
	}()

	out = new(api.Machine)
	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: update, machineID: builder.ID, headers: headers}, builder, out); err != nil {
		return nil, fmt.Errorf("failed to update VM %s: %w", builder.ID, err)
	}
	return out, nil
}

func (f *Client) Start(ctx context.Context, machineID string, nonce string) (out *api.MachineStartResponse, err error) {
	headers := make(map[string][]string)
	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	out = new(api.MachineStartResponse)

	metrics.Started(ctx, "machine_start")
	defer func() {
		metrics.Status(ctx, "machine_start", err == nil)
	}()

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: start, machineID: machineID, headers: headers}, nil, out); err != nil {
		return nil, fmt.Errorf("failed to start VM %s: %w", machineID, err)
	}
	return out, nil
}

type waitQuerystring struct {
	InstanceId     string `url:"instance_id,omitempty"`
	TimeoutSeconds int    `url:"timeout,omitempty"`
	State          string `url:"state,omitempty"`
}

const proxyTimeoutThreshold = 60 * time.Second

func (f *Client) Wait(ctx context.Context, machine *api.Machine, state string, timeout time.Duration) (err error) {
	if state == "" {
		state = "started"
	}
	version := machine.InstanceID
	if machine.Version != "" {
		version = machine.Version
	}
	if timeout > proxyTimeoutThreshold {
		timeout = proxyTimeoutThreshold
	}
	if timeout < 1*time.Second {
		timeout = 1 * time.Second
	}
	timeoutSeconds := int(timeout.Seconds())
	waitQs := waitQuerystring{
		InstanceId:     version,
		TimeoutSeconds: timeoutSeconds,
		State:          state,
	}
	qsVals, err := query.Values(waitQs)
	if err != nil {
		return fmt.Errorf("error making query string for wait request: %w", err)
	}

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: wait, machineID: machine.ID, queryParameters: qsVals}, nil, nil); err != nil {
		return fmt.Errorf("failed to wait for VM %s in %s state: %w", machine.ID, state, err)
	}
	return
}

func (f *Client) Stop(ctx context.Context, in api.StopMachineInput, nonce string) (err error) {
	headers := make(map[string][]string)
	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: stop, machineID: in.ID, headers: headers}, in, nil); err != nil {
		return fmt.Errorf("failed to stop VM %s: %w", in.ID, err)
	}
	return
}

func (f *Client) Restart(ctx context.Context, in api.RestartMachineInput, nonce string) (err error) {
	headers := make(map[string][]string)
	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	queryParams := url.Values{}
	queryParams.Add("force_stop", fmt.Sprint(in.ForceStop))
	if in.Timeout != 0 {
		queryParams.Add("timeout", fmt.Sprint(in.Timeout))
	}
	if in.Signal != "" {
		queryParams.Add("signal", fmt.Sprint(in.Signal))
	}

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: restart, queryParameters: queryParams, machineID: in.ID, headers: headers}, nil, nil); err != nil {
		return fmt.Errorf("failed to restart VM %s: %w", in.ID, err)
	}
	return
}

func (f *Client) Get(ctx context.Context, machineID string) (*api.Machine, error) {
	out := new(api.Machine)

	err := f.sendRequestMachines(ctx, flapsActionInfo{action: get, machineID: machineID}, nil, out)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) GetMany(ctx context.Context, machineIDs []string) ([]*api.Machine, error) {
	machines := make([]*api.Machine, 0, len(machineIDs))
	for _, id := range machineIDs {
		m, err := f.Get(ctx, id)
		if err != nil {
			return machines, err
		}
		machines = append(machines, m)
	}
	return machines, nil
}

func (f *Client) List(ctx context.Context, state string) ([]*api.Machine, error) {
	queryParameters := url.Values{}
	if state != "" {
		queryParameters.Add(state, "")
	}

	out := make([]*api.Machine, 0)

	err := f.sendRequestMachines(ctx, flapsActionInfo{action: list, queryParameters: queryParameters}, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	return out, nil
}

// ListActive returns only non-destroyed that aren't in a reserved process group.
func (f *Client) ListActive(ctx context.Context) ([]*api.Machine, error) {
	machines, err := f.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list active VMs: %w", err)
	}

	machines = lo.Filter(machines, func(m *api.Machine, _ int) bool {
		return !m.IsReleaseCommandMachine() && !m.IsFlyAppsConsole() && m.IsActive()
	})

	return machines, nil
}

// ListFlyAppsMachines returns apps that are part of the Fly apps platform.
// Destroyed machines and console machines are excluded.
// Unlike other List functions, this function retries multiple times.
func (f *Client) ListFlyAppsMachines(ctx context.Context) ([]*api.Machine, *api.Machine, error) {
	var allMachines []*api.Machine
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxElapsedTime = 5 * time.Second
	err := backoff.Retry(func() error {
		var err error
		allMachines, err = f.List(ctx, "")
		if err != nil {
			if errors.Is(err, FlapsErrorNotFound) {
				return err
			} else {
				return backoff.Permanent(err)
			}
		}
		return nil
	}, backoff.WithContext(b, ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list VMs even after retries: %w", err)
	}
	var releaseCmdMachine *api.Machine
	machines := make([]*api.Machine, 0)
	for _, m := range allMachines {
		if m.IsFlyAppsPlatform() && m.IsActive() && !m.IsFlyAppsReleaseCommand() && !m.IsFlyAppsConsole() {
			machines = append(machines, m)
		} else if m.IsFlyAppsReleaseCommand() {
			releaseCmdMachine = m
		}
	}
	return machines, releaseCmdMachine, nil
}

func (f *Client) Destroy(ctx context.Context, input api.RemoveMachineInput, nonce string) (err error) {
	headers := make(map[string][]string)
	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	queryParameters := url.Values{}
	queryParameters.Add("kill", fmt.Sprint(input.Kill))

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: destroy, machineID: input.ID, queryParameters: queryParameters, headers: headers}, nil, nil); err != nil {
		return fmt.Errorf("failed to destroy VM %s: %w", input.ID, err)
	}

	return
}

func (f *Client) Kill(ctx context.Context, machineID string) (err error) {
	in := map[string]interface{}{
		"signal": 9,
	}
	err = f.sendRequestMachines(ctx, flapsActionInfo{action: kill, machineID: machineID}, in, nil)

	if err != nil {
		return fmt.Errorf("failed to kill VM %s: %w", machineID, err)
	}
	return
}

func (f *Client) FindLease(ctx context.Context, machineID string) (*api.MachineLease, error) {
	out := new(api.MachineLease)
	err := f.sendRequestMachines(ctx, flapsActionInfo{action: findLease, machineID: machineID}, nil, out)
	if err != nil {
		return nil, fmt.Errorf("failed to get lease on VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) AcquireLease(ctx context.Context, machineID string, ttl *int) (*api.MachineLease, error) {
	queryParameters := url.Values{}
	if ttl != nil {
		queryParameters.Add("ttl", fmt.Sprint(*ttl))
	}

	out := new(api.MachineLease)
	err := f.sendRequestMachines(ctx, flapsActionInfo{action: acquireLease, machineID: machineID, queryParameters: queryParameters}, nil, out)
	if err != nil {
		return nil, fmt.Errorf("failed to get lease on VM %s: %w", machineID, err)
	}
	terminal.Debugf("got lease on machine %s: %v\n", machineID, out)
	return out, nil
}

func (f *Client) RefreshLease(ctx context.Context, machineID string, ttl *int, nonce string) (*api.MachineLease, error) {
	queryParameters := url.Values{}
	if ttl != nil {
		queryParameters.Add("ttl", fmt.Sprint(*ttl))
	}

	headers := make(map[string][]string)
	headers[NonceHeader] = []string{nonce}
	out := new(api.MachineLease)
	err := f.sendRequestMachines(ctx, flapsActionInfo{action: refreshLease, machineID: machineID, queryParameters: queryParameters, headers: headers}, nil, out)
	if err != nil {
		return nil, fmt.Errorf("failed to get lease on VM %s: %w", machineID, err)
	}
	terminal.Debugf("got lease on machine %s: %v\n", machineID, out)
	return out, nil
}

func (f *Client) ReleaseLease(ctx context.Context, machineID, nonce string) error {
	headers := make(map[string][]string)

	if nonce != "" {
		headers[NonceHeader] = []string{nonce}
	}

	return f.sendRequestMachines(ctx, flapsActionInfo{action: releaseLease, machineID: machineID, headers: headers}, nil, nil)
}

func (f *Client) Exec(ctx context.Context, machineID string, in *api.MachineExecRequest) (*api.MachineExecResponse, error) {
	out := new(api.MachineExecResponse)

	err := f.sendRequestMachines(ctx, flapsActionInfo{action: exec, machineID: machineID}, in, out)
	if err != nil {
		return nil, fmt.Errorf("failed to exec on VM %s: %w", machineID, err)
	}
	return out, nil
}

func (f *Client) GetProcesses(ctx context.Context, machineID string) (api.MachinePsResponse, error) {
	var out api.MachinePsResponse

	err := f.sendRequestMachines(ctx, flapsActionInfo{action: ps, machineID: machineID}, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("failed to get processes from VM %s: %w", machineID, err)
	}

	return out, nil
}

func (f *Client) Cordon(ctx context.Context, machineID string) (err error) {
	metrics.Started(ctx, "machine_cordon")
	sendUpdateMetrics := metrics.StartTiming(ctx, "machine_cordon/duration")
	defer func() {
		metrics.Status(ctx, "machine_cordon", err == nil)
		if err == nil {
			sendUpdateMetrics()
		}
	}()

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: cordon, machineID: machineID}, nil, nil); err != nil {
		return fmt.Errorf("failed to cordon VM: %w", err)
	}

	return nil
}

func (f *Client) Uncordon(ctx context.Context, machineID string) (err error) {
	metrics.Started(ctx, "machine_uncordon")
	sendUpdateMetrics := metrics.StartTiming(ctx, "machine_uncordon/duration")
	defer func() {
		metrics.Status(ctx, "machine_uncordon", err == nil)
		if err == nil {
			sendUpdateMetrics()
		}
	}()

	if err := f.sendRequestMachines(ctx, flapsActionInfo{action: uncordon, machineID: machineID}, nil, nil); err != nil {
		return fmt.Errorf("failed to uncordon VM: %w", err)
	}

	return nil
}
