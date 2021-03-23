package kernelcache

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/blacktop/go-macho"
	"github.com/blacktop/go-macho/pkg/fixupchains"
	"github.com/blacktop/go-plist"
)

const tagPtrMask = 0xffff000000000000

type PrelinkInfo struct {
	PrelinkInfoDictionary []CFBundle `plist:"_PrelinkInfoDictionary,omitempty"`
}

type CFBundle struct {
	ID   string `plist:"CFBundleIdentifier,omitempty"`
	Name string `plist:"CFBundleName,omitempty"`

	SDK                 string   `plist:"DTSDKName,omitempty"`
	SDKBuild            string   `plist:"DTSDKBuild,omitempty"`
	Xcode               string   `plist:"DTXcode,omitempty"`
	XcodeBuild          string   `plist:"DTXcodeBuild,omitempty"`
	Copyright           string   `plist:"NSHumanReadableCopyright,omitempty"`
	BuildMachineOSBuild string   `plist:"BuildMachineOSBuild,omitempty"`
	DevelopmentRegion   string   `plist:"CFBundleDevelopmentRegion,omitempty"`
	PlatformName        string   `plist:"DTPlatformName,omitempty"`
	PlatformVersion     string   `plist:"DTPlatformVersion,omitempty"`
	PlatformBuild       string   `plist:"DTPlatformBuild,omitempty"`
	PackageType         string   `plist:"CFBundlePackageType,omitempty"`
	Version             string   `plist:"CFBundleVersion,omitempty"`
	ShortVersionString  string   `plist:"CFBundleShortVersionString,omitempty"`
	CompatibleVersion   string   `plist:"OSBundleCompatibleVersion,omitempty"`
	MinimumOSVersion    string   `plist:"MinimumOSVersion,omitempty"`
	SupportedPlatforms  []string `plist:"CFBundleSupportedPlatforms,omitempty"`
	Signature           string   `plist:"CFBundleSignature,omitempty"`

	IOKitPersonalities map[string]interface{} `plist:"IOKitPersonalities,omitempty"`
	OSBundleLibraries  map[string]string      `plist:"OSBundleLibraries,omitempty"`
	UIDeviceFamily     []int                  `plist:"UIDeviceFamily,omitempty"`

	OSBundleRequired             string   `plist:"OSBundleRequired,omitempty"`
	UIRequiredDeviceCapabilities []string `plist:"UIRequiredDeviceCapabilities,omitempty"`

	AppleSecurityExtension bool `plist:"AppleSecurityExtension,omitempty"`

	InfoDictionaryVersion string `plist:"CFBundleInfoDictionaryVersion,omitempty"`
	OSKernelResource      bool   `plist:"OSKernelResource,omitempty"`
	GetInfoString         string `plist:"CFBundleGetInfoString,omitempty"`
	AllowUserLoad         bool   `plist:"OSBundleAllowUserLoad,omitempty"`
	ExecutableLoadAddr    uint64 `plist:"_PrelinkExecutableLoadAddr,omitempty"`

	ModuleIndex  uint64 `plist:"ModuleIndex,omitempty"`
	Executable   string `plist:"CFBundleExecutable,omitempty"`
	BundlePath   string `plist:"_PrelinkBundlePath,omitempty"`
	RelativePath string `plist:"_PrelinkExecutableRelativePath,omitempty"`
}

type KmodInfoT struct {
	NextAddr          uint64
	InfoVersion       int32
	ID                uint32
	Name              [64]byte
	Version           [64]byte
	ReferenceCount    int32  // # linkage refs to this
	ReferenceListAddr uint64 // who this refs (links on)
	Address           uint64 // starting address
	Size              uint64 // total size
	HeaderSize        uint64 // unwired hdr size
	StartAddr         uint64
	StopAddr          uint64
}

func (i KmodInfoT) String() string {
	return fmt.Sprintf("id: %#x, name: %s, version: %s, ref_cnt: %d, ref_list: %#x, addr: %#x, size: %#x, header_size: %#x, start: %#x, stop: %#x, next: %#x, info_ver: %d",
		i.ID,
		string(i.Name[:]),
		string(i.Version[:]),
		i.ReferenceCount,
		i.ReferenceListAddr,
		i.Address,
		i.Size,
		i.HeaderSize,
		i.StartAddr,
		i.StopAddr,
		i.NextAddr,
		i.InfoVersion,
	)
}

func getKextStartVMAddrs(m *macho.File) ([]uint64, error) {
	if kmodStart := m.Section("__PRELINK_INFO", "__kmod_start"); kmodStart != nil {
		data, err := kmodStart.Data()
		if err != nil {
			return nil, err
		}
		ptrs := make([]uint64, kmodStart.Size/8)
		if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &ptrs); err != nil {
			return nil, err
		}
		return ptrs, nil
	}
	return nil, fmt.Errorf("section __PRELINK_INFO.__kmod_start not found")
}

func getKextInfos(m *macho.File) ([]KmodInfoT, error) {
	var infos []KmodInfoT
	if kmodStart := m.Section("__PRELINK_INFO", "__kmod_info"); kmodStart != nil {
		data, err := kmodStart.Data()
		if err != nil {
			return nil, err
		}
		ptrs := make([]uint64, kmodStart.Size/8)
		if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &ptrs); err != nil {
			return nil, err
		}
		for _, ptr := range ptrs {
			// fmt.Printf("ptr: %#x, untagged: %#x\n", ptr, unTag(ptr))
			off, err := m.GetOffset(ptr | tagPtrMask)
			if err != nil {
				return nil, err
			}
			info := KmodInfoT{}
			infoBytes := make([]byte, binary.Size(info))
			_, err = m.ReadAt(infoBytes, int64(off))
			if err != nil {
				return nil, err
			}

			if err := binary.Read(bytes.NewReader(infoBytes), binary.LittleEndian, &info); err != nil {
				return nil, fmt.Errorf("failed to read KmodInfoT at %#x: %v", off, err)
			}

			// fixups
			info.StartAddr = fixupchains.DyldChainedPtr64KernelCacheRebase{Pointer: info.StartAddr}.Target() + m.GetBaseAddress()
			info.StopAddr = fixupchains.DyldChainedPtr64KernelCacheRebase{Pointer: info.StopAddr}.Target() + m.GetBaseAddress()

			infos = append(infos, info)
		}
		return infos, nil
	}
	return nil, fmt.Errorf("section __PRELINK_INFO.__kmod_start not found")
}

func GetSandboxOpts(m *macho.File) ([]string, error) {
	var bcOpts []string

	if dconst := m.Section("__DATA_CONST", "__const"); dconst != nil {
		data, err := dconst.Data()
		if err != nil {
			return nil, err
		}
		ptrs := make([]uint64, dconst.Size/8)
		if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &ptrs); err != nil {
			return nil, err
		}
		found := false
		for _, ptr := range ptrs {
			if ptr == 0 {
				continue
			}

			str, err := m.GetCString(ptr | tagPtrMask)
			if err != nil {
				if found {
					break
				}
				continue
			}

			if str == "default" {
				found = true
			}

			if found {
				bcOpts = append(bcOpts, str)
				if getTag(ptr) != 0x17 { // always directly followed by another pointer
					break
				}
			}
		}
	}

	// GetSandboxProfiles(m)

	return bcOpts, nil
}

// TODO: finish this
func GetSandboxProfiles(m *macho.File) ([]byte, error) {
	var profiles []byte

	if tConst := m.Section("__TEXT", "__const"); tConst != nil {
		data, err := tConst.Data()
		if err != nil {
			return nil, err
		}
		index := 0
		for {
			if found := bytes.Index(data[index:], []byte{'\x00', '\x80'}); found != -1 {
				// fmt.Println(hex.Dump(data[index+found : index+found+100]))
				index += found + 1
			} else {
				break
			}
		}
	}

	return profiles, nil
}

func getTag(ptr uint64) uint64 {
	return ptr >> 48
}

func unTag(ptr uint64) uint64 {
	return (ptr & ((1 << 48) - 1)) | (0xffff << 48)
}

// KextList lists all the kernel extensions in the kernelcache
func KextList(kernel string) error {

	m, err := macho.Open(kernel)
	if err != nil {
		return err
	}
	defer m.Close()

	kextStartAdddrs, err := getKextStartVMAddrs(m)
	if err != nil {
		return err
	}

	if infoSec := m.Section("__PRELINK_INFO", "__info"); infoSec != nil {

		data, err := infoSec.Data()
		if err != nil {
			return err
		}

		var prelink PrelinkInfo
		decoder := plist.NewDecoder(bytes.NewReader(bytes.Trim([]byte(data), "\x00")))
		err = decoder.Decode(&prelink)
		if err != nil {
			return err
		}

		fmt.Println("FOUND:", len(prelink.PrelinkInfoDictionary))
		for _, bundle := range prelink.PrelinkInfoDictionary {
			if !bundle.OSKernelResource {
				fmt.Printf("%#x: %s (%s)\n", kextStartAdddrs[bundle.ModuleIndex]|tagPtrMask, bundle.ID, bundle.Version)
			} else {
				fmt.Printf("%#x: %s (%s)\n", 0, bundle.ID, bundle.Version)
			}
		}
	}

	return nil
}
