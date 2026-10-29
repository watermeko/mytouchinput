// Two jobs for the on-screen keyboard.
//
// 1. KWin only delivers touch events to a window that can be activated, so the
//    window rule must *not* set acceptfocus=false.  Accepting focus would then
//    steal it from the text field the keyboard types into, so bounce it back:
//    the moment the keyboard becomes active, reactivate whatever was active
//    before it.  Doing this inside KWin is instant, unlike a D-Bus round trip
//    per keystroke.
//
// 2. A window rule cannot force keepAbove on this compositor, but a script can.

var TARGET = "org.liuqin.TouchInput";
var previous = null;

function dock(c) {
    if (!c || c.resourceClass !== TARGET) return;
    c.keepAbove = true;
    c.noBorder = true;
    c.skipTaskbar = true;
    c.onAllDesktops = true;
}

var clients = workspace.windowList ? workspace.windowList() : workspace.clientList();
for (var i = 0; i < clients.length; i++) dock(clients[i]);

if (workspace.windowAdded) workspace.windowAdded.connect(dock);
else if (workspace.clientAdded) workspace.clientAdded.connect(dock);

if (workspace.windowActivated) {
    workspace.windowActivated.connect(function (w) {
        if (!w) return;
        if (w.resourceClass !== TARGET) {
            previous = w;
            return;
        }
        if (previous && previous !== w) {
            print("BOUNCE focus back to " + previous.resourceClass);
            workspace.activeWindow = previous;
        }
    });
}
