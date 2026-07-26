//go:build darwin && cgo

package tui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

static void clipboardImage(void **bytes, size_t *length) {
	*bytes = NULL;
	*length = 0;
	NSAutoreleasePool *pool = [[NSAutoreleasePool alloc] init];
	NSImage *image = [[NSImage alloc] initWithPasteboard:[NSPasteboard generalPasteboard]];
	if (image == nil) {
		[pool drain];
		return;
	}
	NSRect rect = NSMakeRect(0, 0, image.size.width, image.size.height);
	NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL
		pixelsWide:rect.size.width pixelsHigh:rect.size.height bitsPerSample:8
		samplesPerPixel:4 hasAlpha:YES isPlanar:NO colorSpaceName:NSDeviceRGBColorSpace
		bitmapFormat:NSBitmapFormatAlphaNonpremultiplied bytesPerRow:0 bitsPerPixel:0];
	if (bitmap != nil) {
		[NSGraphicsContext saveGraphicsState];
		[NSGraphicsContext setCurrentContext:[NSGraphicsContext graphicsContextWithBitmapImageRep:bitmap]];
		[image drawInRect:rect];
		[NSGraphicsContext restoreGraphicsState];
		NSData *data = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		if (data != nil && data.length > 0) {
			*bytes = malloc(data.length);
			if (*bytes != NULL) {
				memcpy(*bytes, data.bytes, data.length);
				*length = data.length;
			}
		}
		[bitmap release];
	}
	[image release];
	[pool drain];
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func readClipboardImage() ([]byte, error) {
	var bytes unsafe.Pointer
	var length C.size_t
	C.clipboardImage((*unsafe.Pointer)(&bytes), &length)
	if bytes == nil || length == 0 {
		return nil, errors.New("clipboard does not contain an image")
	}
	defer C.free(bytes)
	return C.GoBytes(bytes, C.int(length)), nil
}
