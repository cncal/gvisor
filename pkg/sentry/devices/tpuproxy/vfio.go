// Copyright 2024 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tpuproxy

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/fdnotifier"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/usermem"
	"gvisor.dev/gvisor/pkg/waiter"
)

// deviceFD implements vfs.FileDescriptionImpl for /dev/vfio/vfio.
type vfioFd struct {
	vfsfd vfs.FileDescription
	vfs.FileDescriptionDefaultImpl
	vfs.DentryMetadataFileDescriptionImpl
	vfs.NoLockFD

	hostFd     int32
	device     *vfioDevice
	queue      waiter.Queue
	memmapFile vfioFDMemmapFile
}

// Release implements vfs.FileDescriptionImpl.Release.
func (fd *vfioFd) Release(context.Context) {
	fdnotifier.RemoveFD(fd.hostFd)
	fd.queue.Notify(waiter.EventHUp)
	unix.Close(int(fd.hostFd))
}

// EventRegister implements waiter.Waitable.EventRegister.
func (fd *vfioFd) EventRegister(e *waiter.Entry) error {
	fd.queue.EventRegister(e)
	if err := fdnotifier.UpdateFD(fd.hostFd); err != nil {
		fd.queue.EventUnregister(e)
		return err
	}
	return nil
}

// EventUnregister implements waiter.Waitable.EventUnregister.
func (fd *vfioFd) EventUnregister(e *waiter.Entry) {
	fd.queue.EventUnregister(e)
	if err := fdnotifier.UpdateFD(fd.hostFd); err != nil {
		panic(fmt.Sprint("UpdateFD:", err))
	}
}

// Readiness implements waiter.Waitable.Readiness.
func (fd *vfioFd) Readiness(mask waiter.EventMask) waiter.EventMask {
	return fdnotifier.NonBlockingPoll(fd.hostFd, mask)
}

// Epollable implements vfs.FileDescriptionImpl.Epollable.
func (fd *vfioFd) Epollable() bool {
	return true
}

// Ioctl implements vfs.FileDescriptionImpl.Ioctl.
func (fd *vfioFd) Ioctl(ctx context.Context, uio usermem.IO, sysno uintptr, args arch.SyscallArguments) (uintptr, error) {
	cmd := args[1].Uint()
	t := kernel.TaskFromContext(ctx)
	if t == nil {
		panic("Ioctl should be called from a task context")
	}
	switch cmd {
	case linux.VFIO_CHECK_EXTENSION:
		return fd.checkExtension(extension(args[2].Int()))
	}
	return 0, linuxerr.ENOSYS
}

// checkExtension returns a positive integer when the given VFIO extension
// is supported, otherwise, it returns 0.
func (fd *vfioFd) checkExtension(ext extension) (uintptr, error) {
	switch ext {
	case linux.VFIO_TYPE1_IOMMU, linux.VFIO_SPAPR_TCE_IOMMU, linux.VFIO_TYPE1v2_IOMMU:
		ret, err := ioctlInvoke[int32](fd.hostFd, linux.VFIO_CHECK_EXTENSION, int32(ext))
		if err != nil {
			log.Warningf("check VFIO extension %s: %v", ext, err)
			return 0, err
		}
		return ret, nil
	}
	return 0, linuxerr.EINVAL
}

// VFIO extension.
type extension int32

// String implements fmt.Stringer for VFIO extension string representation.
func (e extension) String() string {
	switch e {
	case linux.VFIO_TYPE1_IOMMU:
		return "VFIO_TYPE1_IOMMU"
	case linux.VFIO_SPAPR_TCE_IOMMU:
		return "VFIO_SPAPR_TCE_IOMMU"
	case linux.VFIO_TYPE1v2_IOMMU:
		return "VFIO_TYPE1v2_IOMMU"
	}
	return ""
}
