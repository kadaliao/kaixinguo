//go:build windows

// Package imageio decodes the pictures Go's image packages can't, such as
// HEIC covers, with the Windows Imaging Component, called through syscall
// (no cgo). WIC reads HEIC with the system's HEIF Image Extensions, which
// decode its pictures with the HEVC Video Extensions.
package imageio

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"syscall"
	"unsafe"
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidWICImagingFactory = guid{0xcacaf262, 0x9370, 0x4615, [8]byte{0xa1, 0x3b, 0x9f, 0x55, 0x39, 0xda, 0x4c, 0x0a}}
	iidIWICImagingFactory  = guid{0xec5ec8a9, 0xc395, 0x4314, [8]byte{0x9c, 0x77, 0x54, 0xd7, 0xa9, 0x35, 0xff, 0x70}}
	// Premultiplied BGRA, the format every WIC scaler takes.
	pixelFormat32bppPBGRA = guid{0x6fddc324, 0x4e03, 0x4bfe, [8]byte{0xb1, 0x85, 0x3d, 0x77, 0x76, 0x8d, 0xc9, 0x10}}

	ole32                  = syscall.NewLazyDLL("ole32.dll")
	coInitializeEx         = ole32.NewProc("CoInitializeEx")
	coUninitialize         = ole32.NewProc("CoUninitialize")
	coCreateInstance       = ole32.NewProc("CoCreateInstance")
	windowscodecs          = syscall.NewLazyDLL("windowscodecs.dll")
	wicConvertBitmapSource = windowscodecs.NewProc("WICConvertBitmapSource")

	errNoDecoder = errors.New("imageio: no decoder for this format; HEIC needs the HEIF and HEVC extensions")
)

const (
	sFalse                 = 1          // S_FALSE: COM was set up on the thread already
	rpcEChangedMode        = 0x80010106 // RPC_E_CHANGED_MODE: in another apartment
	wincodecErrNoComponent = 0x88982F50 // WINCODEC_ERR_COMPONENTNOTFOUND

	coinitMultithreaded         = 0
	clsctxInprocServer          = 1
	decodeMetadataCacheOnDemand = 0
	interpolationFant           = 3
)

// The methods called, by their place in their interface's vtable, which
// starts with IUnknown's QueryInterface, AddRef and Release.
const (
	release = 2

	factoryCreateDecoderFromStream = 4
	factoryCreateBitmapScaler      = 11
	factoryCreateStream            = 14

	streamInitializeFromMemory = 16

	decoderGetFrame = 13

	sourceGetSize    = 3
	sourceCopyPixels = 7

	scalerInitialize = 8
)

// hresult is a failed COM call's result.
type hresult uint32

func (hr hresult) Error() string { return fmt.Sprintf("imageio: HRESULT 0x%08X", uint32(hr)) }

// check is the error of a COM call's result, nil when it succeeded. It
// takes what a syscall returns, of which the result is the first.
func check(r, _ uintptr, _ error) error {
	if int32(r) < 0 {
		return hresult(r)
	}
	return nil
}

// object is a COM interface pointer: memory that starts with its vtable.
type object struct{ vtbl *[32]uintptr }

// call calls the object's method m with args, after the object itself.
//
//go:uintptrescapes
func (o *object) call(m int, args ...uintptr) error {
	return check(syscall.SyscallN(o.vtbl[m], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...))
}

func (o *object) release() {
	_, _, _ = syscall.SyscallN(o.vtbl[release], uintptr(unsafe.Pointer(o)))
}

// Decode decodes a picture of any format WIC reads into RGBA pixels,
// scaled down to at most maxWidth pixels wide when maxWidth > 0.
func Decode(data []byte, maxWidth int) (*image.RGBA, error) {
	if len(data) == 0 {
		return nil, errors.New("imageio: no data")
	}
	if err := ole32.Load(); err != nil {
		return nil, err
	}
	if err := windowscodecs.Load(); err != nil {
		return nil, err
	}
	// COM is set up per thread, so the goroutine keeps to one while it uses
	// it. The multithreaded apartment takes any thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch r, _, _ := coInitializeEx.Call(0, coinitMultithreaded); r {
	case 0, sFalse:
		defer coUninitialize.Call()
	case rpcEChangedMode:
		// The thread is in another apartment, which serves as well.
	default:
		return nil, hresult(r)
	}
	img, err := decode(data, maxWidth)
	runtime.KeepAlive(data) // the stream reads it in place
	return img, err
}

func decode(data []byte, maxWidth int) (*image.RGBA, error) {
	var factory *object
	if err := check(coCreateInstance.Call(uintptr(unsafe.Pointer(&clsidWICImagingFactory)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIWICImagingFactory)), uintptr(unsafe.Pointer(&factory)))); err != nil {
		return nil, err
	}
	defer factory.release()

	var stream *object
	if err := factory.call(factoryCreateStream, uintptr(unsafe.Pointer(&stream))); err != nil {
		return nil, err
	}
	defer stream.release()
	if err := stream.call(streamInitializeFromMemory, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data))); err != nil {
		return nil, err
	}

	var decoder *object
	if err := factory.call(factoryCreateDecoderFromStream, uintptr(unsafe.Pointer(stream)), 0,
		decodeMetadataCacheOnDemand, uintptr(unsafe.Pointer(&decoder))); err != nil {
		if errors.Is(err, hresult(wincodecErrNoComponent)) {
			return nil, errNoDecoder
		}
		return nil, err
	}
	defer decoder.release()
	var frame *object
	if err := decoder.call(decoderGetFrame, 0, uintptr(unsafe.Pointer(&frame))); err != nil {
		return nil, err
	}
	defer frame.release()

	// The frame as premultiplied BGRA, whatever it is stored as.
	var src *object
	if err := check(wicConvertBitmapSource.Call(uintptr(unsafe.Pointer(&pixelFormat32bppPBGRA)),
		uintptr(unsafe.Pointer(frame)), uintptr(unsafe.Pointer(&src)))); err != nil {
		return nil, err
	}
	defer src.release()

	var w, h uint32
	if err := src.call(sourceGetSize, uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&h))); err != nil {
		return nil, err
	}
	if w == 0 || h == 0 {
		return nil, errors.New("imageio: empty image")
	}
	if maxWidth > 0 && int(w) > maxWidth {
		h = uint32(max(1, int(h)*maxWidth/int(w)))
		w = uint32(maxWidth)
		var scaler *object
		if err := factory.call(factoryCreateBitmapScaler, uintptr(unsafe.Pointer(&scaler))); err != nil {
			return nil, err
		}
		defer scaler.release()
		if err := scaler.call(scalerInitialize, uintptr(unsafe.Pointer(src)), uintptr(w), uintptr(h), interpolationFant); err != nil {
			return nil, err
		}
		src = scaler
	}

	out := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	if err := src.call(sourceCopyPixels, 0, uintptr(out.Stride), uintptr(len(out.Pix)), uintptr(unsafe.Pointer(&out.Pix[0]))); err != nil {
		return nil, err
	}
	// BGRA to RGBA; both are premultiplied, as image.RGBA keeps its pixels.
	for i := 0; i < len(out.Pix); i += 4 {
		out.Pix[i], out.Pix[i+2] = out.Pix[i+2], out.Pix[i]
	}
	return out, nil
}
