namespace CageTenant;

internal static class Program
{
    [STAThread]
    private static int Main()
    {
        // Variables such as WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS could add
        // --ignore-certificate-errors or a debugging pipe; WebView2 and its
        // browser processes inherit this process's environment.
        foreach (var name in WebPolicy.WebView2VariablesToClear(Environment.GetEnvironmentVariables()))
            Environment.SetEnvironmentVariable(name, null);

        using var mutex = new Mutex(true, @"Local\io.github.sweetcornna.claude-cage.tenant", out var first);
        ApplicationConfiguration.Initialize();
        if (!first)
        {
            MessageBox.Show("Cage Tenant 已在运行。", "Cage Tenant", MessageBoxButtons.OK, MessageBoxIcon.Information);
            return 0;
        }
        Application.Run(new MainForm(PortalStore.ForCurrentUser()));
        return 0;
    }
}
