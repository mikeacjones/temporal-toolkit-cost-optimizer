package cloudauth

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/cloud-sdk/api/cloudservice/v1"
	namespacev1 "go.temporal.io/cloud-sdk/api/namespace/v1"
	usagev1 "go.temporal.io/cloud-sdk/api/usage/v1"
	"go.temporal.io/cloud-sdk/cloudclient"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Ops reads account and Namespace settings through the Cloud Ops API. It
// only reads; tcopt's config checks never change anything.
type Ops struct {
	client *cloudclient.Client
	owned  bool // closed by Close when Ops opened the client itself
}

// Ops returns a reader that acts as the logged-in user.
func (s *Session) Ops() *Ops { return &Ops{client: s.client} }

// NewOpsWithAPIKey opens a Cloud Ops API client with an API key. User and
// service account keys both work, with that identity's permissions.
func NewOpsWithAPIKey(key string) (*Ops, error) {
	c, err := cloudclient.New(cloudclient.Options{APIKey: key, UserAgent: "tcopt"})
	if err != nil {
		return nil, err
	}
	return &Ops{client: c, owned: true}, nil
}

// Close closes a client that NewOpsWithAPIKey opened.
func (o *Ops) Close() {
	if o.owned {
		o.client.Close()
	}
}

// Namespace returns one Namespace's spec and status.
func (o *Ops) Namespace(ctx context.Context, name string) (*namespacev1.Namespace, error) {
	resp, err := o.client.CloudService().GetNamespace(ctx, &cloudservice.GetNamespaceRequest{Namespace: name})
	if err != nil {
		return nil, fmt.Errorf("get Namespace %s: %w", name, err)
	}
	return resp.GetNamespace(), nil
}

// AllNamespaces returns every Namespace the caller can see.
func (o *Ops) AllNamespaces(ctx context.Context) ([]*namespacev1.Namespace, error) {
	var out []*namespacev1.Namespace
	token := ""
	for {
		resp, err := o.client.CloudService().GetNamespaces(ctx, &cloudservice.GetNamespacesRequest{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, fmt.Errorf("list Namespaces: %w", err)
		}
		out = append(out, resp.GetNamespaces()...)
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// ExportSinks returns a Namespace's Workflow History Export sinks.
func (o *Ops) ExportSinks(ctx context.Context, namespace string) ([]*namespacev1.ExportSink, error) {
	var out []*namespacev1.ExportSink
	token := ""
	for {
		resp, err := o.client.CloudService().GetNamespaceExportSinks(ctx, &cloudservice.GetNamespaceExportSinksRequest{
			Namespace: namespace, PageSize: 100, PageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list export sinks for %s: %w", namespace, err)
		}
		out = append(out, resp.GetSinks()...)
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// Usage returns the account's daily usage summaries from start up to end.
// The caller needs permission to read usage; without it the API returns
// PermissionDenied.
func (o *Ops) Usage(ctx context.Context, start, end time.Time) ([]*usagev1.Summary, error) {
	var out []*usagev1.Summary
	token := ""
	for {
		resp, err := o.client.CloudService().GetUsage(ctx, &cloudservice.GetUsageRequest{
			StartTimeInclusive: timestamppb.New(start), EndTimeExclusive: timestamppb.New(end),
			PageSize: 1000, PageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("get usage: %w", err)
		}
		out = append(out, resp.GetSummaries()...)
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}
