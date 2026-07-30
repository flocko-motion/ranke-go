// package: tests/backends / integration
// type:    tool
// job:     shared podman lifecycle for the ephemeral service pods — remove a container together
// with the anonymous volumes its image declared
// limits:  containers the suite started; a host-native service is never torn down (-> redis_pod,
// minio_pod, neo4j_pod)
package backends

import "os/exec"

// removePod deletes the container and the anonymous volumes it owns. The service
// images declare VOLUME, so a plain rm leaves one behind per run.
func removePod(name string) {
	_ = exec.Command("podman", "rm", "-f", "-v", name).Run()
}
