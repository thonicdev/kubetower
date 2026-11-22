package launch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/sharedstate"
	"github.com/thonicdev/kubetower/internal/sharedstate/valkeystore"
)

// Several replicas behind one address, and what that asks of this process.
//
// KT_REPLICATED=true says this process is one of several behind one Service: a
// pod cannot see its Deployment's replica count, which an autoscaler changes
// anyway.
//
// KT_VALKEY_ADDR is the store every replica shares: single sign-on sessions and
// confirmation plans live there instead of in memory. A sign-in in progress is a
// sealed cookie, carried across replicas by the shared session key. It may be
// set without KT_REPLICATED: one replica with a shared store and a stable
// KT_SESSION_SECRET keeps sessions across a restart (without the secret, a
// restart draws a new key and the stored records cannot be opened).
//
// Several replicas refuse to start without a shared store, outside the server
// profile, without KT_SESSION_SECRET (each would refuse the others' cookies), or
// with a shared password but no KT_PASSWORD_HASH (each would draw its own).
// Each of these would start and then behave differently per pod, diagnosed only
// as "sometimes it signs me out".
//
// What only one pod can hold is absent, decided when the router is built
// (api.Deps.Replicated): preference and settings writes and the per-cluster
// extension switches (files in one pod's state directory), and port-forwards
// (listeners in one pod). The archive is off, since each pod would hold half.

const (
	envReplicated     = "KT_REPLICATED"
	envValkeyAddr     = "KT_VALKEY_ADDR"
	envValkeyUsername = "KT_VALKEY_USERNAME"
	envValkeyPassword = "KT_VALKEY_PASSWORD"
)

// replication is what the environment says about replicas and the shared store.
type replication struct {
	// Several is KT_REPLICATED.
	Several bool
	// Store is the Valkey store, unsealed, or nil for this process's memory;
	// frontDoor seals it with the session key before use.
	Store sharedstate.Store
}

// readReplication reads the two settings and refuses the combinations that could
// never work. passwordOffered is whether the shared password is a way in, which
// decides whether a drawn one would be a divergence.
func readReplication(inCluster, passwordOffered bool, getenv func(string) string) (replication, error) {
	several := false
	if raw := strings.TrimSpace(getenv(envReplicated)); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			several = true
		case "0", "false", "no", "off":
		default:
			return replication{}, fmt.Errorf("%s=%q is neither true nor false, and whether this process "+
				"is one of several decides what it offers", envReplicated, raw)
		}
	}
	addr := strings.TrimSpace(getenv(envValkeyAddr))

	if several {
		var missing []string
		if !inCluster {
			missing = append(missing, "the server profile (--incluster): the desktop profile is one "+
				"operator's console, and several of it are several consoles")
		}
		if addr == "" {
			missing = append(missing, envValkeyAddr+": sessions and confirmation plans would each live "+
				"in one pod")
		}
		if len(strings.TrimSpace(getenv("KT_SESSION_SECRET"))) < 32 {
			missing = append(missing, "KT_SESSION_SECRET (32 bytes or more): each replica would draw its "+
				"own signing key and refuse the other's cookies")
		}
		if passwordOffered && strings.TrimSpace(getenv("KT_PASSWORD_HASH")) == "" {
			missing = append(missing, "KT_PASSWORD_HASH: each replica would draw its own password into "+
				"its own state directory")
		}
		if len(missing) > 0 {
			return replication{}, fmt.Errorf("%s is set, and several replicas need %s",
				envReplicated, strings.Join(missing, "; and "))
		}
	}
	if addr == "" {
		return replication{Several: several}, nil
	}

	store, err := valkeystore.New(valkeystore.Config{
		Address:  addr,
		Username: strings.TrimSpace(getenv(envValkeyUsername)),
		Password: getenv(envValkeyPassword),
	})
	if store == nil {
		return replication{}, err
	}
	// A credentials refusal is not a store starting up: waiting fixes nothing.
	// Refused at start-up, where a crash-looping pod with this line is visible
	// and a running pod answering 503 is not.
	if errors.Is(err, valkeystore.ErrCredentials) {
		return replication{}, err
	}
	if err != nil {
		// Not fatal: Valkey and these pods start together, and refusing would
		// crash-loop every install. Requests needing the store answer 503 until
		// it answers.
		log.Printf("[kubetower] %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = store.Ping(ctx)
		cancel()
		if errors.Is(err, valkeystore.ErrCredentials) {
			return replication{}, fmt.Errorf("%v. Check KT_VALKEY_PASSWORD, and KT_VALKEY_USERNAME if one is set",
				err)
		}
		if err != nil {
			log.Printf("[kubetower] the shared store is configured and did not answer yet: %v. Requests "+
				"that need it answer 503 until it does", err)
		}
	}
	// The address and the kind; the password's presence only.
	password := "no password"
	if getenv(envValkeyPassword) != "" {
		password = "a password"
	}
	log.Printf("[kubetower] shared store: %s, with %s", store.Describe(), password)
	return replication{Several: several, Store: store}, nil
}

// passwordOfferedByEnv is the shared-password rule read from the environment
// before auth.New runs: a way in without an identity provider, and with one
// only where KT_AUTH_LOCAL_ACCOUNT kept it.
func passwordOfferedByEnv() bool {
	if strings.TrimSpace(os.Getenv("KT_OIDC_ISSUER")) == "" {
		return true
	}
	return envBool("KT_AUTH_LOCAL_ACCOUNT", false)
}
