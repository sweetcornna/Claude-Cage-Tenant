package io.github.leexeo.cage.mobile;

import android.content.SharedPreferences;
import android.net.Uri;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;
import org.json.JSONArray;
import org.json.JSONObject;

// Saved Cage server accounts. A remembered password is sealed with an Android
// Keystore AES key; the portal and user name also authenticate the ciphertext.
final class ServerAccounts {
    static final class Account {
        final String portal, user;
        Account(String portal, String user) { this.portal = portal; this.user = user; }
        String id() { return portal + "\n" + user; }
        String host() { String host = Uri.parse(portal).getHost(); return host == null ? portal : host; }
        @Override public boolean equals(Object other) {
            return other instanceof Account && ((Account) other).portal.equals(portal) && ((Account) other).user.equals(user);
        }
        @Override public int hashCode() { return id().hashCode(); }
    }
    private static final String ALIAS = "cage-mobile-server-password-aes-v1";
    private static final String LIST = "accounts-v1";
    private final SharedPreferences prefs;
    ServerAccounts(SharedPreferences prefs) { this.prefs = prefs; }

    private JSONArray records() {
        try { return new JSONArray(prefs.getString(LIST, "[]")); } catch (org.json.JSONException e) { return new JSONArray(); }
    }
    synchronized List<Account> list() {
        List<Account> result = new ArrayList<>();
        JSONArray all = records();
        for (int i = 0; i < all.length(); i++) {
            JSONObject item = all.optJSONObject(i);
            if (item != null) result.add(new Account(item.optString("portal"), item.optString("user")));
        }
        return result;
    }
    private SecretKey key(boolean create) throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore");
        store.load(null);
        if (store.containsAlias(ALIAS)) return (SecretKey) store.getKey(ALIAS, null);
        if (!create) throw new IOException("设备凭据不可用。");
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
        generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
            .setKeySize(256).setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());
        return generator.generateKey();
    }
    private static byte[] context(Account account) {
        return ("Cage server v1\n" + account.portal + "\n" + account.user).getBytes(StandardCharsets.UTF_8);
    }
    synchronized void save(Account account, String password) throws Exception {
        JSONArray all = records(), next = new JSONArray();
        for (int i = 0; i < all.length(); i++) {
            JSONObject item = all.optJSONObject(i);
            if (item != null && !(account.portal.equals(item.optString("portal")) && account.user.equals(item.optString("user")))) next.put(item);
        }
        JSONObject record = new JSONObject().put("portal", account.portal).put("user", account.user);
        if (password != null) {
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.ENCRYPT_MODE, key(true));
            cipher.updateAAD(context(account));
            byte[] plain = password.getBytes(StandardCharsets.UTF_8);
            byte[] sealed;
            try { sealed = cipher.doFinal(plain); } finally { Arrays.fill(plain, (byte) 0); }
            record.put("password", Base64.encodeToString(cipher.getIV(), Base64.NO_WRAP) + "." + Base64.encodeToString(sealed, Base64.NO_WRAP));
        }
        next.put(record);
        if (!prefs.edit().putString(LIST, next.toString()).commit()) throw new IOException("无法保存服务器。");
    }
    synchronized String password(Account account) {
        try {
            JSONArray all = records();
            for (int i = 0; i < all.length(); i++) {
                JSONObject item = all.optJSONObject(i);
                if (item == null || !account.portal.equals(item.optString("portal")) || !account.user.equals(item.optString("user"))) continue;
                String[] parts = item.optString("password").split("\\.", -1);
                if (parts.length != 2) return null;
                Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
                cipher.init(Cipher.DECRYPT_MODE, key(false), new GCMParameterSpec(128, Base64.decode(parts[0], Base64.NO_WRAP)));
                cipher.updateAAD(context(account));
                byte[] plain = cipher.doFinal(Base64.decode(parts[1], Base64.NO_WRAP));
                try { return new String(plain, StandardCharsets.UTF_8); } finally { Arrays.fill(plain, (byte) 0); }
            }
        } catch (Exception ignored) {}
        return null;
    }
    synchronized void forget(Account account) throws Exception {
        JSONArray all = records(), next = new JSONArray();
        for (int i = 0; i < all.length(); i++) {
            JSONObject item = all.optJSONObject(i);
            if (item != null && !(account.portal.equals(item.optString("portal")) && account.user.equals(item.optString("user")))) next.put(item);
        }
        if (!prefs.edit().putString(LIST, next.toString()).commit()) throw new IOException("无法移除服务器。");
    }
}
