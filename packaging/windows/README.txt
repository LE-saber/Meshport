Meshport for Windows
====================

1. Unzip this folder anywhere (for example %LOCALAPPDATA%\Programs\Meshport).
2. Run meshport.exe. The app opens in a Microsoft Edge app window and puts an
   icon in the notification area (system tray).
3. Enter the server address and the enrollment code from your administrator.

Data (identity keys, history, logs): %APPDATA%\Meshport
Received files: %USERPROFILE%\Downloads\Meshport (changeable in Settings)

Command line (run from a terminal; output is not shown because this is a GUI
build, use the UI or the log file in the data folder instead):
  meshport.exe --minimized      start in the tray only
  meshport.exe setup --server https://mesh.example.com --enroll mpe_...
  meshport.exe --headless       no window / tray

Windows Defender SmartScreen may warn on first start because the binary is
not code-signed. Choose "More info" -> "Run anyway", or sign the binary in
your release pipeline.
