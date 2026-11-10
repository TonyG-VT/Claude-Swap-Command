package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework UserNotifications
#import <AppKit/AppKit.h>
#import <UserNotifications/UserNotifications.h>
#include <stdlib.h>

static NSString *csc_string(const char *text) {
	NSString *s = text == NULL ? nil : [NSString stringWithUTF8String:text];
	return s == nil ? @"" : s;
}

// csc_alert shows one window with a title, a text and up to three buttons, and
// returns which button was pressed (0 is the first, which Enter presses; a
// button named Cancel is pressed by Esc). It returns -1 when the window closed
// by itself. detail, when given, is shown in a box that scrolls and whose text
// can be selected. It must not be called before the app's event loop runs.
static int csc_alert(const char *title, const char *text, const char *detail, const char *b1, const char *b2, const char *b3, int style, double closeAfter) {
	__block int result = -1;
	void (^show)(void) = ^{
		@autoreleasepool {
			@try {
				NSAlert *alert = [[NSAlert alloc] init];
				alert.messageText = csc_string(title);
				alert.informativeText = csc_string(text);
				alert.alertStyle = style == 2 ? NSAlertStyleCritical : (style == 1 ? NSAlertStyleWarning : NSAlertStyleInformational);
				const char *titles[3] = {b1, b2, b3};
				for (int i = 0; i < 3; i++) {
					if (titles[i] != NULL) {
						[alert addButtonWithTitle:csc_string(titles[i])];
					}
				}
				if (detail != NULL) {
					NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, 600, 360)];
					scroll.hasVerticalScroller = YES;
					scroll.borderType = NSBezelBorder;
					NSSize content = [scroll contentSize];
					NSTextView *view = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, content.width, content.height)];
					view.editable = NO;
					view.selectable = YES;
					view.font = [NSFont systemFontOfSize:13];
					view.textContainerInset = NSMakeSize(8, 8);
					view.minSize = NSMakeSize(0, content.height);
					view.maxSize = NSMakeSize(FLT_MAX, FLT_MAX);
					view.verticallyResizable = YES;
					view.horizontallyResizable = NO;
					view.autoresizingMask = NSViewWidthSizable;
					view.textContainer.containerSize = NSMakeSize(content.width, FLT_MAX);
					view.textContainer.widthTracksTextView = YES;
					view.string = csc_string(detail);
					scroll.documentView = view;
					alert.accessoryView = scroll;
				}
				NSTimer *timer = nil;
				if (closeAfter > 0) {
					timer = [NSTimer timerWithTimeInterval:closeAfter repeats:NO block:^(NSTimer *t) {
						[NSApp abortModal];
					}];
					[[NSRunLoop currentRunLoop] addTimer:timer forMode:NSModalPanelRunLoopMode];
				}
				[NSApp activateIgnoringOtherApps:YES];
				[[alert window] setLevel:NSFloatingWindowLevel];
				NSModalResponse response = [alert runModal];
				[timer invalidate];
				if (response >= NSAlertFirstButtonReturn && response < NSAlertFirstButtonReturn + 3) {
					result = (int)(response - NSAlertFirstButtonReturn);
				}
			} @catch (NSException *exception) {
				result = -2;
			}
		}
	};
	if ([NSThread isMainThread]) {
		show();
	} else {
		dispatch_sync(dispatch_get_main_queue(), show);
	}
	return result;
}

static void csc_copy(const char *text) {
	NSString *s = csc_string(text);
	dispatch_async(dispatch_get_main_queue(), ^{
		NSPasteboard *board = [NSPasteboard generalPasteboard];
		[board clearContents];
		[board setString:s forType:NSPasteboardTypeString];
	});
}

@interface CSCNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation CSCNotificationDelegate
// Show the message even while one of the helper's own windows is in front.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
	completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}
@end

static CSCNotificationDelegate *cscNotificationDelegate;

// csc_notifications is the notification centre, or nil when this process is
// not running from an app bundle (macOS has no notifications for bare tools).
static UNUserNotificationCenter *csc_notifications(void) {
	if ([[NSBundle mainBundle] bundleIdentifier] == nil) {
		return nil;
	}
	@try {
		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		static dispatch_once_t once;
		dispatch_once(&once, ^{
			cscNotificationDelegate = [[CSCNotificationDelegate alloc] init];
			center.delegate = cscNotificationDelegate;
		});
		return center;
	} @catch (NSException *exception) {
		return nil;
	}
}

// csc_notify_prepare asks, once, for permission to show notifications, so the
// macOS question appears when the helper starts and not during a switch.
static void csc_notify_prepare(void) {
	UNUserNotificationCenter *center = csc_notifications();
	if (center == nil) {
		return;
	}
	[center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
	                      completionHandler:^(BOOL granted, NSError *error) {}];
}

// csc_notify shows a notification. It returns 1 when macOS took it, 0 when
// notifications are not available or not allowed.
static int csc_notify(const char *title, const char *body) {
	UNUserNotificationCenter *center = csc_notifications();
	if (center == nil) {
		return 0;
	}
	NSString *t = csc_string(title);
	NSString *b = csc_string(body);
	__block int allowed = 0;
	dispatch_semaphore_t answered = dispatch_semaphore_create(0);
	[center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
		allowed = settings.authorizationStatus == UNAuthorizationStatusAuthorized ||
		          settings.authorizationStatus == UNAuthorizationStatusProvisional;
		dispatch_semaphore_signal(answered);
	}];
	if (dispatch_semaphore_wait(answered, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || !allowed) {
		return 0;
	}
	UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
	content.title = t;
	content.body = b;
	UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString] content:content trigger:nil];
	__block int delivered = 0;
	dispatch_semaphore_t added = dispatch_semaphore_create(0);
	[center addNotificationRequest:request withCompletionHandler:^(NSError *error) {
		delivered = error == nil;
		dispatch_semaphore_signal(added);
	}];
	if (dispatch_semaphore_wait(added, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0) {
		return 0;
	}
	return delivered;
}
*/
import "C"

import (
	"time"
	"unsafe"
)

const appTitle = "Claude Swap Command"

type alertStyle int

const (
	alertInfo alertStyle = iota
	alertWarning
)

// showAlert shows one window and returns the index of the button pressed, or
// -1 when it closed by itself. Enter presses the first button; Esc presses a
// button named Cancel. Only the menu-bar app may call it.
func showAlert(style alertStyle, title, text, detail string, closeAfter time.Duration, buttons ...string) int {
	cTitle, cText := C.CString(title), C.CString(text)
	defer C.free(unsafe.Pointer(cTitle))
	defer C.free(unsafe.Pointer(cText))
	var cDetail *C.char
	if detail != "" {
		cDetail = C.CString(detail)
		defer C.free(unsafe.Pointer(cDetail))
	}
	var cButtons [3]*C.char
	for i := range cButtons {
		if i < len(buttons) {
			cButtons[i] = C.CString(buttons[i])
			defer C.free(unsafe.Pointer(cButtons[i]))
		}
	}
	return int(C.csc_alert(cTitle, cText, cDetail, cButtons[0], cButtons[1], cButtons[2], C.int(style), C.double(closeAfter.Seconds())))
}

// confirmDialog asks one question. ok is the name of the button that goes ahead.
func confirmDialog(title, text, ok string) bool {
	return showAlert(alertInfo, title, text, "", 0, ok, "Cancel") == 0
}

// problemDialog reports something that did not work.
func problemDialog(title, text string) {
	showAlert(alertWarning, title, text, "", 0, "OK")
}

func copyToClipboard(text string) {
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.csc_copy(cText)
}

func prepareNotifications() { C.csc_notify_prepare() }

// notifyUser shows a short message as a macOS notification. When
// notifications are switched off for the helper, the message is shown in a
// small window that closes by itself instead, so it is never lost.
func notifyUser(title, body string) {
	cTitle, cBody := C.CString(title), C.CString(body)
	defer C.free(unsafe.Pointer(cTitle))
	defer C.free(unsafe.Pointer(cBody))
	if C.csc_notify(cTitle, cBody) == 1 {
		return
	}
	showAlert(alertInfo, title, body, "", 12*time.Second, "OK")
}
