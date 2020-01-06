package tenantdb_test

import (
	"github.com/diamanticom/multi-tenancy/tenant/tenantdb"
	"reflect"
	"testing"
)

// Ensure that a database can be opened without error.
func TestOpen(t *testing.T) {
	tenantdb.ConnecttoBolt()
}

func TestStore(t *testing.T) {
	targetkubeconfig := make([]string, 0)
	targetkubeconfig = append(targetkubeconfig, "Hello1")
	masterkubeconfig := make([]string, 0)
	masterkubeconfig = append(masterkubeconfig, "Hello1")
	msa := make([]string, 0)
	msa = append(msa, "Hello1")
	tsa := make([]string, 0)
	tsa = append(tsa, "Hello1")
	temp := tenantdb.TenantData{MasterKubeConfig: masterkubeconfig,
		TargetKubeConfig: targetkubeconfig,
		MasterSA:         msa,
		TargetSA:         tsa,
	}
	tenantdb.TenantStoreKey("Test", &temp)
}

func TestGet(t *testing.T) {

	arb := tenantdb.TenantGetKey("Test")
	targetkubeconfig := make([]string, 0)
	targetkubeconfig = append(targetkubeconfig, "Hello1")
	masterkubeconfig := make([]string, 0)
	masterkubeconfig = append(masterkubeconfig, "Hello1")
	msa := make([]string, 0)
	msa = append(msa, "Hello1")
	tsa := make([]string, 0)
	tsa = append(tsa, "Hello1")
	temp := tenantdb.TenantData{MasterKubeConfig: masterkubeconfig,
		TargetKubeConfig: targetkubeconfig,
		MasterSA:         msa,
		TargetSA:         tsa,
	}
	if reflect.DeepEqual(*arb, temp) == false {
		t.Fail()
	}
}

func TestDeleteKey(t *testing.T) {
	tenantdb.TenantDeleteKey("Test")
	arb := tenantdb.TenantGetKey("Test")
	if arb != nil {
		t.Fail()
	}
}
