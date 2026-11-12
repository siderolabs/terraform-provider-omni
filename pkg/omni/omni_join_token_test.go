// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func TestParseJoinTokenExpiration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		want    time.Time
		name    string
		value   string
		wantErr string
	}{
		{
			name:  "UTC",
			value: "2027-01-01T00:00:00Z",
			want:  time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "surrounding whitespace is tolerated",
			value: "  2027-01-01T00:00:00Z  ",
			want:  time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// An offset is the same instant written another way, which is what the attribute means.
			name:  "offset is accepted and is the same instant",
			value: "2027-01-01T01:00:00+01:00",
			want:  time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// Omni puts no ceiling on a join token's lifetime, unlike a service account key.
			name:  "far future is allowed",
			value: "2099-12-31T23:59:59Z",
			want:  time.Date(2099, time.December, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			// The past is parsed here; rejecting it belongs to the apply, which knows "now".
			name:  "the past parses",
			value: "2000-01-01T00:00:00Z",
			want:  time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "a duration is not a timestamp",
			value:   "720h",
			wantErr: `"720h" is not an RFC 3339 timestamp`,
		},
		{
			name:    "date without a time",
			value:   "2027-01-01",
			wantErr: `"2027-01-01" is not an RFC 3339 timestamp`,
		},
		{
			name:    "no zone",
			value:   "2027-01-01T00:00:00",
			wantErr: `is not an RFC 3339 timestamp`,
		},
		{
			name:    "empty",
			value:   "",
			wantErr: `is not an RFC 3339 timestamp`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := omni.ParseJoinTokenExpiration(test.value)

			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)

				return
			}

			require.NoError(t, err)
			assert.True(t, got.Equal(test.want), "got %s, want %s", got, test.want)
		})
	}
}

func TestReconcileTimestamp(t *testing.T) {
	t.Parallel()

	instant := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		server   *timestamppb.Timestamp
		name     string
		existing types.String
		want     types.String
	}{
		{
			name:     "an unset timestamp is null, not the zero time",
			server:   nil,
			existing: types.StringValue("2027-01-01T00:00:00Z"),
			want:     types.StringNull(),
		},
		{
			name:     "no prior value takes the server's spelling",
			server:   timestamppb.New(instant),
			existing: types.StringNull(),
			want:     types.StringValue("2027-01-01T00:00:00Z"),
		},
		{
			// The whole point: an offset and a Z are the same instant, and rewriting one as the
			// other would be a diff that is textual only.
			name:     "an equivalent spelling is preserved",
			server:   timestamppb.New(instant),
			existing: types.StringValue("2027-01-01T01:00:00+01:00"),
			want:     types.StringValue("2027-01-01T01:00:00+01:00"),
		},
		{
			name:     "a genuinely different instant is replaced",
			server:   timestamppb.New(instant),
			existing: types.StringValue("2028-06-01T00:00:00Z"),
			want:     types.StringValue("2027-01-01T00:00:00Z"),
		},
		{
			name:     "an unparseable prior value is replaced",
			server:   timestamppb.New(instant),
			existing: types.StringValue("720h"),
			want:     types.StringValue("2027-01-01T00:00:00Z"),
		},
		{
			name:     "an unknown prior value is replaced",
			server:   timestamppb.New(instant),
			existing: types.StringUnknown(),
			want:     types.StringValue("2027-01-01T00:00:00Z"),
		},
		{
			// The attribute accepts surrounding whitespace, and the planned value must come back
			// verbatim or Terraform reports an inconsistent result after apply.
			name:     "surrounding whitespace is preserved",
			server:   timestamppb.New(instant),
			existing: types.StringValue("  2027-01-01T00:00:00Z  "),
			want:     types.StringValue("  2027-01-01T00:00:00Z  "),
		},
		{
			name:     "fractional seconds are kept",
			server:   timestamppb.New(instant.Add(123456789 * time.Nanosecond)),
			existing: types.StringNull(),
			want:     types.StringValue("2027-01-01T00:00:00.123456789Z"),
		},
		{
			name:     "a subsecond change is not hidden",
			server:   timestamppb.New(instant.Add(500 * time.Millisecond)),
			existing: types.StringValue("2027-01-01T00:00:00Z"),
			want:     types.StringValue("2027-01-01T00:00:00.5Z"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.ReconcileTimestamp(test.existing, test.server))
		})
	}
}

func TestJoinURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		baseURL string
		token   string
		want    string
	}{
		{
			name:    "no existing query",
			baseURL: "https://omni.example.com:8090",
			token:   "abc",
			want:    "https://omni.example.com:8090?jointoken=abc",
		},
		{
			name:    "existing query is appended to",
			baseURL: "https://omni.example.com:8090?grpc_tunnel=true",
			token:   "abc",
			want:    "https://omni.example.com:8090?grpc_tunnel=true&jointoken=abc",
		},
		{
			name:    "trailing path is preserved",
			baseURL: "https://omni.example.com/api",
			token:   "abc",
			want:    "https://omni.example.com/api?jointoken=abc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.JoinURL(test.baseURL, test.token))
		})
	}
}

func TestPlannedJoinTokenExpiration(t *testing.T) {
	t.Parallel()

	const (
		past   = "2001-01-01T00:00:00Z"
		future = "2999-01-01T00:00:00Z"
	)

	for _, test := range []struct {
		planned types.String
		prior   types.String
		name    string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "no expiration",
			planned: types.StringNull(),
			prior:   types.StringNull(),
			wantNil: true,
		},
		{
			name:    "a future expiration on create",
			planned: types.StringValue(future),
			prior:   types.StringNull(),
		},
		{
			name:    "a past expiration on create",
			planned: types.StringValue(past),
			prior:   types.StringNull(),
			wantErr: true,
		},
		{
			name:    "a changed expiration moved into the past",
			planned: types.StringValue(past),
			prior:   types.StringValue(future),
			wantErr: true,
		},
		{
			// Revoking a token that has already expired keeps its expiration as it was.
			name:    "an unchanged past expiration",
			planned: types.StringValue(past),
			prior:   types.StringValue(past),
		},
		{
			name:    "an unchanged past expiration spelled differently",
			planned: types.StringValue("2001-01-01T01:00:00+01:00"),
			prior:   types.StringValue(past),
		},
		{
			name:    "an unparseable expiration",
			planned: types.StringValue("720h"),
			prior:   types.StringNull(),
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var diags diag.Diagnostics

			got, ok := omni.PlannedJoinTokenExpiration(test.planned, test.prior, &diags)

			if test.wantErr {
				assert.False(t, ok)
				assert.True(t, diags.HasError())

				return
			}

			require.True(t, ok, "%v", diags)
			assert.False(t, diags.HasError())
			assert.Equal(t, test.wantNil, got == nil)
		})
	}
}

func TestJoinEndpoint(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		baseURL    string
		token      string
		want       string
		kernelArgs []string
	}{
		{
			name: "taken from the siderolink.api kernel argument",
			kernelArgs: []string{
				"siderolink.api=https://omni.example.com:8090?grpc_tunnel=true&jointoken=abc",
				"talos.events.sink=[fdae:41e4:649b:9303::1]:8090",
			},
			baseURL: "https://omni.example.com:8090",
			token:   "abc",
			want:    "https://omni.example.com:8090?grpc_tunnel=true&jointoken=abc",
		},
		{
			name:       "rebuilt when the kernel argument is missing",
			kernelArgs: []string{"talos.events.sink=[fdae:41e4:649b:9303::1]:8090"},
			baseURL:    "https://omni.example.com:8090",
			token:      "abc",
			want:       "https://omni.example.com:8090?jointoken=abc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.JoinEndpoint(test.kernelArgs, test.baseURL, test.token))
		})
	}
}
