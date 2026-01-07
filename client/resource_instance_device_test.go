package client

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/suite"
)

// DeviceSnapshotSuite tests device output against saved snapshots.
type DeviceSnapshotSuite struct {
	suite.Suite
	snapshotter *cupaloy.Config
}

// DeviceTestCase represents a single device test case.
type DeviceTestCase struct {
	Name   string
	Device InstanceDevice
}

func (s *DeviceSnapshotSuite) SetupSuite() {
	s.snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots")))
}

func (s *DeviceSnapshotSuite) runDeviceTest(tc DeviceTestCase) {
	s.Run(tc.Name, func() {
		name, config, err := tc.Device.ToIncusDevice()
		s.Require().Nil(err)

		result := map[string]any{
			"name":   name,
			"config": config,
		}

		output, jsonErr := json.MarshalIndent(result, "", "  ")
		s.Require().NoError(jsonErr)

		s.snapshotter.SnapshotT(s.T(), string(output))
	})
}

func (s *DeviceSnapshotSuite) TestProxyDevices() {
	testCases := []DeviceTestCase{
		{
			Name: "proxy_basic_tcp",
			Device: InstanceDevice{
				Name: "proxy-8080",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  "0.0.0.0",
						ListenPort:  8080,
						ConnectType: "tcp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 80,
					},
				},
			},
		},
		{
			Name: "proxy_udp",
			Device: InstanceDevice{
				Name: "proxy-5353",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "udp",
						ListenAddr:  "0.0.0.0",
						ListenPort:  5353,
						ConnectType: "udp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 53,
					},
				},
			},
		},
		{
			Name: "proxy_custom_listen_addr",
			Device: InstanceDevice{
				Name: "proxy-3000",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  "192.168.1.100",
						ListenPort:  3000,
						ConnectType: "tcp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 3000,
					},
				},
			},
		},
		{
			Name: "proxy_with_nat",
			Device: InstanceDevice{
				Name: "proxy-443",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  "0.0.0.0",
						ListenPort:  443,
						ConnectType: "tcp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 8443,
						Nat:         true,
					},
				},
			},
		},
		{
			Name: "proxy_high_port",
			Device: InstanceDevice{
				Name: "proxy-65535",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  "0.0.0.0",
						ListenPort:  65535,
						ConnectType: "tcp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 65535,
					},
				},
			},
		},
		{
			Name: "proxy_port_zero",
			Device: InstanceDevice{
				Name: "proxy-0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeProxy,
					Proxy: InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  "0.0.0.0",
						ListenPort:  0,
						ConnectType: "tcp",
						ConnectAddr: "127.0.0.1",
						ConnectPort: 0,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		s.runDeviceTest(tc)
	}
}

func (s *DeviceSnapshotSuite) TestDiskDevices() {
	testCases := []DeviceTestCase{
		{
			Name: "disk_volume",
			Device: InstanceDevice{
				Name: "vol-data",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeDisk,
					Disk: InstanceDeviceDiskConfig{
						StorageVolumeConfig: &StorageVolumeConfig{Pool: "default"},
						Source:              "my-volume",
						Path:                "/data",
					},
				},
			},
		},
		{
			Name: "disk_bind_mount",
			Device: InstanceDevice{
				Name: "bind-config",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeDisk,
					Disk: InstanceDeviceDiskConfig{
						Source: "/host/config",
						Path:   "/app/config",
						Shift:  true,
					},
				},
			},
		},
		{
			Name: "disk_readonly",
			Device: InstanceDevice{
				Name: "bind-static",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeDisk,
					Disk: InstanceDeviceDiskConfig{
						Source:   "/host/static",
						Path:     "/app/static",
						ReadOnly: true,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		s.runDeviceTest(tc)
	}
}

func (s *DeviceSnapshotSuite) TestNicDevices() {
	testCases := []DeviceTestCase{
		{
			Name: "nic_basic",
			Device: InstanceDevice{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeNic,
					Network:    newMockResource("my-network", KindNetwork, PriorityNetwork, true),
				},
			},
		},
	}

	for _, tc := range testCases {
		s.runDeviceTest(tc)
	}
}

func (s *DeviceSnapshotSuite) TestTmpfsDevices() {
	testCases := []DeviceTestCase{
		{
			Name: "tmpfs_basic",
			Device: InstanceDevice{
				Name: "tmpfs--tmp",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeTmpfs,
					Tmpfs: InstanceDeviceTmpfsConfig{
						Path: "/tmp",
					},
				},
			},
		},
		{
			Name: "tmpfs_with_size",
			Device: InstanceDevice{
				Name: "tmpfs--run",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeTmpfs,
					Tmpfs: InstanceDeviceTmpfsConfig{
						Path: "/run",
						Size: "104857600",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		s.runDeviceTest(tc)
	}
}

func (s *DeviceSnapshotSuite) TestDeviceErrors() {
	s.Run("nic_no_network", func() {
		device := InstanceDevice{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
			},
		}
		_, _, err := device.ToIncusDevice()
		s.Error(err)
	})

	s.Run("unknown_no_extra", func() {
		device := InstanceDevice{
			Name: "custom",
			Config: InstanceDeviceConfig{
				DeviceType: "custom-type",
			},
		}
		_, _, err := device.ToIncusDevice()
		s.Error(err)
	})
}

func TestDeviceSnapshotSuite(t *testing.T) {
	suite.Run(t, new(DeviceSnapshotSuite))
}
