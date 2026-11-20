"""XTest input for an isolated Xvfb display; no browser permission APIs."""
import ctypes
import sys
import time

x = ctypes.CDLL('libX11.so.6')
t = ctypes.CDLL('libXtst.so.6')
x.XOpenDisplay.restype = ctypes.c_void_p
x.XStringToKeysym.argtypes = [ctypes.c_char_p]
x.XStringToKeysym.restype = ctypes.c_ulong
x.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
x.XKeysymToKeycode.restype = ctypes.c_uint
x.XFlush.argtypes = [ctypes.c_void_p]
t.XTestFakeKeyEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
t.XTestFakeMotionEvent.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_ulong]
t.XTestFakeButtonEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
d = x.XOpenDisplay(None)
if not d:
    raise RuntimeError('No owned X display')
for key in sys.argv[1:]:
    if key.startswith('click:'):
        a, b = map(int, key.split(':')[1:])
        t.XTestFakeMotionEvent(d, -1, a, b, 0)
        t.XTestFakeButtonEvent(d, 1, 1, 0)
        t.XTestFakeButtonEvent(d, 1, 0, 0)
    else:
        code = x.XKeysymToKeycode(d, x.XStringToKeysym(key.encode()))
        if not code:
            raise ValueError('Unknown key')
        t.XTestFakeKeyEvent(d, code, 1, 0)
        t.XTestFakeKeyEvent(d, code, 0, 0)
    x.XFlush(d)
    time.sleep(.15)
