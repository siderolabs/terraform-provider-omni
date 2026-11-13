// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	cosiresource "github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// errPlanInvalid aborts a state update when the plan cannot be converted; the real cause is carried
// in the diagnostics the apply callback wrote.
var errPlanInvalid = errors.New("invalid plan")

// updateWithDiags wraps safe.StateUpdateWithConflicts for resources whose apply step reports
// failures through diagnostics instead of an error return. An apply failure aborts the update and
// keeps the diagnostics it produced; any other error is reported under summary.
func updateWithDiags[T cosiresource.Resource](
	ctx context.Context, st cosistate.State, ptr cosiresource.Pointer, diags *diag.Diagnostics, summary string, apply func(T),
) {
	_, err := safe.StateUpdateWithConflicts(ctx, st, ptr, func(res T) error {
		apply(res)

		if diags.HasError() {
			return errPlanInvalid
		}

		return nil
	})
	if err != nil && !errors.Is(err, errPlanInvalid) {
		errToDiag(diags, summary, err)
	}
}

// providerDataFromResource extracts the shared providerData set by the provider's Configure method.
//
// It returns nil (without adding a diagnostic) when data is nil, which happens during the
// framework's initial validation pass before the provider is configured.
func providerDataFromResource(in any, diags *diag.Diagnostics) *providerData {
	if in == nil {
		return nil
	}

	data, ok := in.(*providerData)
	if !ok {
		diags.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *providerData, got %T. This is a bug in the provider.", in),
		)

		return nil
	}

	return data
}

// errToDiag appends a Go error to the diagnostics as an error with the given summary.
func errToDiag(diags *diag.Diagnostics, summary string, err error) {
	diags.AddError(summary, err.Error())
}

// optionalString maps a server-side string onto an Optional (non-Computed) string attribute.
//
// The server reports the zero value for fields the user left unset, so the empty string is mapped
// back to null to avoid a permanent ""-vs-null diff against the configuration.
func optionalString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}

	return types.StringValue(value)
}

// optionalSet maps a server-side slice onto an Optional (non-Computed) set attribute.
//
// Like optionalString, an empty value maps back to null: the server reports nothing for a field the
// user left unset, and an empty set in state would diff forever against a null configuration.
func optionalSet(ctx context.Context, values []string, diags *diag.Diagnostics) types.Set {
	if len(values) == 0 {
		return types.SetNull(types.StringType)
	}

	set, d := types.SetValueFrom(ctx, types.StringType, values)
	diags.Append(d...)

	return set
}

// optionalList maps a server-side slice onto an Optional (non-Computed) list attribute.
func optionalList(ctx context.Context, values []string, diags *diag.Diagnostics) types.List {
	if len(values) == 0 {
		return types.ListNull(types.StringType)
	}

	list, d := types.ListValueFrom(ctx, types.StringType, values)
	diags.Append(d...)

	return list
}

// optionalMap maps a server-side map onto an Optional (non-Computed) map attribute.
func optionalMap(ctx context.Context, values map[string]string, diags *diag.Diagnostics) types.Map {
	if len(values) == 0 {
		return types.MapNull(types.StringType)
	}

	m, d := types.MapValueFrom(ctx, types.StringType, values)
	diags.Append(d...)

	return m
}

// reconcileDuration returns a string representation of serverDuration that round-trips cleanly
// against the value already in state.
//
// Durations are stored on the server as a normalized value, so the human-friendly form a user
// writes (e.g. "1h") differs textually from the server's form ("1h0m0s") even though they are
// equal. To avoid spurious diffs, the existing state value is preserved whenever it parses to the
// same duration as the server.
func reconcileDuration(existing types.String, serverDuration time.Duration) types.String {
	if serverDuration == 0 {
		return types.StringNull()
	}

	if !existing.IsNull() && !existing.IsUnknown() {
		if parsed, err := time.ParseDuration(existing.ValueString()); err == nil && parsed == serverDuration {
			return existing
		}
	}

	return types.StringValue(serverDuration.String())
}

// reconcileTimestamp returns a string representation of serverTime that round-trips cleanly against
// the value already in state, mapping an unset timestamp to null rather than to the zero time.
//
// RFC 3339 admits several spellings of the same instant: a trailing `Z` and a `+00:00` offset are
// the same moment, as is that moment written in any other zone. The server reports its own
// spelling, so the existing value is preserved whenever it parses to the same instant, to avoid a
// diff that is textual only. Surrounding whitespace is ignored for the comparison, matching what the
// attribute accepts, but the existing value is kept verbatim so the result matches the plan.
func reconcileTimestamp(existing types.String, serverTime *timestamppb.Timestamp) types.String {
	if serverTime == nil || !serverTime.IsValid() || serverTime.AsTime().IsZero() {
		return types.StringNull()
	}

	instant := serverTime.AsTime()

	if !existing.IsNull() && !existing.IsUnknown() {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(existing.ValueString())); err == nil && parsed.Equal(instant) {
			return existing
		}
	}

	// RFC3339Nano keeps any fractional seconds, so the value recorded is the server's exact instant,
	// while dropping trailing zeros keeps a whole-second instant in its plain form.
	return types.StringValue(instant.UTC().Format(time.RFC3339Nano))
}

// enumDisplayName renders a protobuf enum value name as an attribute value.
//
// Omni's protos disagree on casing: some enums are SCREAMING_SNAKE, others are already CamelCase.
// A fully uppercase name is rewritten to CamelCase so every enum this provider surfaces reads the
// same way; anything else is already in that shape and is passed through untouched, which also
// keeps a name this build has never seen from being mangled.
func enumDisplayName(name string) string {
	if name != strings.ToUpper(name) {
		return name
	}

	parts := strings.Split(name, "_")

	for i, part := range parts {
		if part == "" {
			continue
		}

		// Enum names are ASCII identifiers, so indexing by byte is safe here.
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}

	return strings.Join(parts, "")
}

// enumName renders one value of a protobuf enum.
//
// A value Omni gained after this provider was built has no name in the generated map, and the empty
// string would read as "no value" rather than "a value we do not know", so it falls back to the
// enum's zero value, which protobuf convention reserves for exactly that.
func enumName(nameMap map[int32]string, number int32) string {
	name, ok := nameMap[number]
	if !ok {
		name = nameMap[0]
	}

	return enumDisplayName(name)
}

// enumNames returns every value of a protobuf enum, rendered, in enum order.
//
// The order is the enum's own rather than Go's randomized map iteration, so a list built from this
// is stable across runs and does not reshuffle the generated documentation.
func enumNames(nameMap map[int32]string) []string {
	names := make([]string, 0, len(nameMap))

	for _, number := range slices.Sorted(maps.Keys(nameMap)) {
		names = append(names, enumDisplayName(nameMap[number]))
	}

	return names
}

// describeOneOf renders a value list for a schema description, e.g. "One of: `A`, `B`.".
func describeOneOf(names []string) string {
	quoted := make([]string, 0, len(names))

	for _, name := range names {
		quoted = append(quoted, "`"+name+"`")
	}

	return "One of: " + strings.Join(quoted, ", ") + "."
}
