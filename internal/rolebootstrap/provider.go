package rolebootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// SecretReader returns a secret's string value.
type SecretReader interface {
	// SecretString returns the current value of the secret with this ARN.
	SecretString(ctx context.Context, arn string) (string, error)
}

// WorkerInvoker invokes the worker Lambda synchronously.
type WorkerInvoker interface {
	// Invoke returns the response payload, or an error if the invocation itself failed. A function
	// error (the worker returned an error) is reported as a non-nil error that carries the worker's
	// message, never the request.
	Invoke(ctx context.Context, functionName string, payload []byte) ([]byte, error)
}

// Provider runs the bootstrap for a custom resource event.
type Provider struct {
	secrets SecretReader
	worker  WorkerInvoker
}

// NewProvider returns a Provider. Both dependencies are required.
func NewProvider(secrets SecretReader, worker WorkerInvoker) (*Provider, error) {
	if secrets == nil || worker == nil {
		return nil, errors.New("rolebootstrap provider: secret reader and worker invoker are required")
	}
	return &Provider{secrets: secrets, worker: worker}, nil
}

// Properties are the custom resource's properties (all strings, as CloudFormation sends them).
type Properties struct {
	MasterSecretArn string
	WorkerFunction  string
	Host            string
	Port            string
	Database        string
}

// PropertiesFrom reads the custom resource properties and reports every missing one at once.
func PropertiesFrom(raw map[string]any) (Properties, error) {
	get := func(key string) string {
		v, _ := raw[key].(string)
		return v
	}
	p := Properties{
		MasterSecretArn: get("MasterSecretArn"), WorkerFunction: get("WorkerFunction"),
		Host: get("Host"), Port: get("Port"), Database: get("Database"),
	}
	var missing []string
	for name, v := range map[string]string{
		"MasterSecretArn": p.MasterSecretArn, "WorkerFunction": p.WorkerFunction,
		"Host": p.Host, "Port": p.Port, "Database": p.Database,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return Properties{}, fmt.Errorf("custom resource properties missing: %v", missing)
	}
	return p, nil
}

// Ensure reads the master secret and has the worker create the roles. It returns the role names.
func (p *Provider) Ensure(ctx context.Context, props Properties) ([]string, error) {
	port, err := strconv.Atoi(props.Port)
	if err != nil {
		return nil, fmt.Errorf("property Port must be a number: %w", err)
	}
	secretString, err := p.secrets.SecretString(ctx, props.MasterSecretArn)
	if err != nil {
		return nil, fmt.Errorf("read master secret: %w", err)
	}
	secret, err := parseMasterSecret(secretString)
	if err != nil {
		return nil, err
	}
	req := Request{Host: props.Host, Port: port, Database: props.Database, Username: secret.Username, Password: secret.Password}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// The worker needs the password; it travels only in this direct invocation payload (ADR-0001 amendment).
	payload, err := json.Marshal(req) //nolint:gosec // G117: intentional, see above; never logged or stored.
	if err != nil {
		return nil, fmt.Errorf("encode worker request: %w", err)
	}
	out, err := p.worker.Invoke(ctx, props.WorkerFunction, payload)
	if err != nil {
		return nil, fmt.Errorf("invoke worker %s: %w", props.WorkerFunction, err)
	}
	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decode worker response: %w", err)
	}
	if resp.Status != StatusOK {
		return nil, fmt.Errorf("worker reported status %q", resp.Status)
	}
	return resp.Roles, nil
}
