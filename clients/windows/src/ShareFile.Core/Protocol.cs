using System.Net;
using System.Text.RegularExpressions;

namespace ShareFile.Core;

public static partial class Protocol
{
    [GeneratedRegex(@"\A[A-Za-z0-9_-]{43}\z")]
    private static partial Regex IdentifierPattern();
    [GeneratedRegex(@"\A/api/v1/uploads/[A-Za-z0-9_-]{43}\z")]
    private static partial Regex UploadPattern();
    [GeneratedRegex(@"\A/s/[A-Za-z0-9_-]{43}\z")]
    private static partial Regex SharePattern();

    public static bool IsIdentifier(string? value) => value is not null && IdentifierPattern().IsMatch(value);
    public static string RequireIdentifier(string id) => IsIdentifier(id) ? id : throw new ClientException(Localization.Text("Некорректный идентификатор"));

    public static string NormalizeOrigin(string input)
    {
        if (!Uri.TryCreate(input.Trim(), UriKind.Absolute, out var uri) ||
            uri.Host.Length == 0 || uri.UserInfo.Length != 0 || uri.AbsolutePath != "/" ||
            uri.Query.Length != 0 || uri.Fragment.Length != 0)
            throw new ClientException(Localization.Text("Введите адрес сервера без пути, логина и параметров"));
        var host = uri.IdnHost.Trim('[', ']');
        var loopback = host.Equals("localhost", StringComparison.OrdinalIgnoreCase) ||
            (IPAddress.TryParse(host, out var ip) && IPAddress.IsLoopback(ip));
        if (uri.Scheme != "https" && !(uri.Scheme == "http" && loopback))
            throw new ClientException(Localization.Text("Нужен HTTPS; HTTP разрешён только для локального тестирования"));
        return uri.GetLeftPart(UriPartial.Authority);
    }

    public static string UploadPath(string location)
    {
        // Use only the advertised resource path, always on the trusted connection origin.
        if (location.StartsWith('/') && UploadPattern().IsMatch(location)) return location;
        if (Uri.TryCreate(location, UriKind.Absolute, out var uri) &&
            uri.UserInfo.Length == 0 && uri.Query.Length == 0 && uri.Fragment.Length == 0 &&
            UploadPattern().IsMatch(uri.AbsolutePath) && !location.Contains('%')) return uri.AbsolutePath;
        throw new ClientException(Localization.Text("Сервер вернул некорректный адрес загрузки"));
    }

    public static void ValidatePairing(Pairing pairing, string origin)
    {
        if (!IsIdentifier(pairing.Request.Id) || string.IsNullOrEmpty(pairing.Request.Secret) ||
            !Uri.TryCreate(pairing.VerificationUrl, UriKind.Absolute, out var uri) ||
            uri.GetLeftPart(UriPartial.Authority) != origin || uri.UserInfo.Length != 0 ||
            uri.AbsolutePath != "/device/connect" || uri.Fragment.Length != 0 ||
            uri.Query != "?request=" + pairing.Request.Id)
            throw new ClientException(Localization.Text("Сервер вернул некорректный адрес подключения"));
    }

    public static void ValidateLink(ShareLink link)
    {
        // PublicURL may differ from the LAN API origin; accept a validated HTTPS capability.
        if (!Uri.TryCreate(link.Url, UriKind.Absolute, out var uri) ||
            NormalizeOrigin(uri.GetLeftPart(UriPartial.Authority)) != uri.GetLeftPart(UriPartial.Authority) ||
            uri.UserInfo.Length != 0 || uri.Query.Length != 0 || uri.Fragment.Length != 0 ||
            !SharePattern().IsMatch(uri.AbsolutePath) || !IsIdentifier(link.Share.Id))
            throw new ClientException(Localization.Text("Сервер вернул некорректную публичную ссылку"));
    }
}
