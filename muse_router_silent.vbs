Set WshShell = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
currentDir = fso.GetParentFolderName(WScript.ScriptFullName)
batPath = currentDir & "\muse_router_service_wrapper.bat"
WshShell.Run "cmd.exe /c """ & batPath & """", 0, False
