package windowsapi

import (
	"testing"
)

func TestQueryServiceName(t *testing.T) {
	pid := uint32(1592) // service pid or svchost pid
	tag := uint32(249)     // service tag

	got, err := QueryServiceNameByTag(pid, tag)
	t.Logf("QueryServiceNameByTag(%d, %d) = %q, err=%v", pid, tag, got, err)

	services, err := GetRunningServicesSnapshot()
	t.Logf("GetRunningServicesSnapshot() = %v, err=%v", services, err)

}
