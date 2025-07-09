package types

type DockerVolumeInfo struct {
	Name       string
	Mountpoint string
	Size       int64
	SizeHuman  string
}

type PVCInfo struct {
	Name          string
	Namespace     string
	RequestedSize string
	MatchedVolume *DockerVolumeInfo
	NewSize       string
}
