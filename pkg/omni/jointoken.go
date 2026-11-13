// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"
)

// parseJoinTokenExpiration parses the `expiration` attribute.
//
// Omni stores an absolute instant rather than a lifetime, so the attribute is the instant itself and
// this only has to reject what cannot mean anything. Omni puts no ceiling on how far out it may be,
// unlike a service account key.
func parseJoinTokenExpiration(value string) (time.Time, error) {
	expiration, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 timestamp (e.g. %q): %w", value, joinTokenExpirationExample, err)
	}

	return expiration, nil
}

// joinTokenExpirationExample is the spelling the `expiration` diagnostics point at.
const joinTokenExpirationExample = "2027-01-01T00:00:00Z"

// joinTokenNameMaxLength mirrors Omni's own limit on a join token name. Enforcing it here turns what
// would be an InvalidArgument at apply time into a plan-time error.
const joinTokenNameMaxLength = 16

// joinTokenNamePattern rejects the control characters Omni refuses in a join token name.
var joinTokenNamePattern = regexp.MustCompile(`^[^\p{Cc}]+$`)

// errJoinTokenNameNotFound reports a `token_name` that matches no join token.
var errJoinTokenNameNotFound = errors.New("join token name not found")

// joinTokenNameInitial is the name omnictl gives the token Omni bootstraps itself with, which is
// stored with an empty name.
const joinTokenNameInitial = "initial"

// resolveJoinToken turns the token selection a caller made into a token ID.
//
// Exactly one of id and name is expected to be set; with neither, the instance default is used. This
// mirrors the --token-id / --token-name pair on `omnictl jointoken`.
func resolveJoinToken(ctx context.Context, st cosistate.State, id, name string) (string, error) {
	switch {
	case id != "":
		// Confirm the token exists rather than handing back an ID that would fail later inside Omni
		// with a less obvious error.
		if _, err := safe.ReaderGetByID[*siderolink.JoinToken](ctx, st, id); err != nil {
			return "", err
		}

		return id, nil
	case name != "":
		tokens, err := safe.ReaderListAll[*siderolink.JoinToken](ctx, st)
		if err != nil {
			return "", err
		}

		for token := range tokens.All() {
			if token.TypedSpec().Value.GetName() == name {
				return token.Metadata().ID(), nil
			}

			// The token Omni bootstraps itself with carries no name, and omnictl calls it "initial".
			if name == joinTokenNameInitial && token.TypedSpec().Value.GetName() == "" {
				return token.Metadata().ID(), nil
			}
		}

		return "", fmt.Errorf("%w: %q", errJoinTokenNameNotFound, name)
	default:
		defaultToken, err := safe.ReaderGetByID[*siderolink.DefaultJoinToken](ctx, st, siderolink.DefaultJoinTokenID)
		if err != nil {
			return "", err
		}

		return defaultToken.TypedSpec().Value.GetTokenId(), nil
	}
}
