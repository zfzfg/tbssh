@{
    RootModule        = 'TerminalBrowserSsh.psm1'
    ModuleVersion     = '0.1.0'
    GUID              = '5b7c2f0e-8a8e-4f43-9d0b-2f6a51c7e1a4'
    Author            = 'tbssh'
    Description       = 'ssh for Windows Terminal that shows terminal-browser (kitty graphics) running on the remote host, translated to sixel.'
    PowerShellVersion = '7.2'
    CompatiblePSEditions = @('Core')
    FunctionsToExport = @('tbssh', 'Enable-TbSshShim', 'Disable-TbSshShim', 'Test-TbSsh', 'Show-TbSshDemo', 'Install-TbSshRemote', 'Uninstall-TbSshRemote')
    CmdletsToExport   = @()
    VariablesToExport = @()
    AliasesToExport   = @()
}
