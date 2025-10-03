package windowsapi

import (
	"testing"
)

func TestQueryServiceName(t *testing.T) {
	pid := uint32(42928) // service pid or svchost pid
	tag := uint32(2)     // service tag

	got, err := QueryServiceNameByTag(pid, tag)
	t.Logf("QueryServiceNameByTag(%d, %d) = %q, err=%v", pid, tag, got, err)

	got = ResolveServiceName(pid, tag)
	t.Logf("ResolveServiceName(%d, %d) = %q", pid, tag, got)

	got, err = queryServiceNameByPid(pid)
	t.Logf("queryServiceNameByPid(%d) = %q, err=%v", pid, got, err)


}
