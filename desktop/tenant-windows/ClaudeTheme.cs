using Microsoft.Win32;

namespace CageTenant;

internal sealed record ClaudePalette(Color Background, Color Surface, Color Border, Color Text, Color SecondaryText,
    Color AccentFill, Color AccentHover, Color AccentText, Color Danger, Color DangerSoft);

/// <summary>The portal's warm Claude palette for the native pages, light or dark per Windows settings.</summary>
internal static class ClaudeTheme
{
    public static readonly ClaudePalette Light = new(Hex(0xF5F3EB), Hex(0xFFFDF8), Hex(0xCFC9BB), Hex(0x292621), Hex(0x6E685D),
        Hex(0xD97757), Hex(0xCF6B4B), Hex(0x261713), Hex(0xA82D2D), Hex(0xF6E1DC));
    public static readonly ClaudePalette Dark = new(Hex(0x22211E), Hex(0x2B2925), Hex(0x575248), Hex(0xF3EFE5), Hex(0xB7B1A5),
        Hex(0xE48C70), Hex(0xED9D83), Hex(0x261713), Hex(0xF27474), Hex(0x45292A));

    public static ClaudePalette Current()
    {
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize");
            return key?.GetValue("AppsUseLightTheme") is int value && value == 0 ? Dark : Light;
        }
        catch (Exception e) when (e is System.Security.SecurityException or UnauthorizedAccessException or IOException)
        {
            return Light;
        }
    }

    public static void Primary(Button button, ClaudePalette palette, bool large = false)
    {
        Base(button, large);
        button.BackColor = palette.AccentFill;
        button.ForeColor = palette.AccentText;
        button.FlatAppearance.BorderSize = 0;
        button.FlatAppearance.MouseOverBackColor = palette.AccentHover;
        button.FlatAppearance.MouseDownBackColor = palette.AccentHover;
    }

    public static void Secondary(Button button, ClaudePalette palette, bool large = false)
    {
        Base(button, large);
        button.BackColor = palette.Surface;
        button.ForeColor = palette.Text;
        button.FlatAppearance.BorderSize = 1;
        button.FlatAppearance.BorderColor = palette.Border;
        button.FlatAppearance.MouseOverBackColor = palette.Background;
        button.FlatAppearance.MouseDownBackColor = palette.Background;
    }

    private static void Base(Button button, bool large)
    {
        button.FlatStyle = FlatStyle.Flat;
        button.UseVisualStyleBackColor = false;
        button.Cursor = Cursors.Hand;
        button.Font = new Font(button.Font.FontFamily, large ? 11f : 9.5f, FontStyle.Bold);
        button.Padding = new Padding(12, 4, 12, 4);
        if (large)
        {
            button.AutoSize = false;
            button.Size = new Size(200, 48);
        }
        else
        {
            button.MinimumSize = new Size(0, 32);
        }
    }

    private static Color Hex(int value) => Color.FromArgb((value >> 16) & 0xFF, (value >> 8) & 0xFF, value & 0xFF);
}
