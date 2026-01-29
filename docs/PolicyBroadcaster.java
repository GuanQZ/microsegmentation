import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.URL;
import java.nio.charset.StandardCharsets;

public class PolicyBroadcaster {

    /**
     * 将策略广播到 headless Service 下的所有 Pod IP。
     *
     * @param headlessDns headless Service 的域名，例如：
     *                    microsegmentation-api-headless.microsegmentation.svc.cluster.local
     * @param port        服务端口，默认 18080
     * @param policyJson  策略 JSON 字符串（与 /apply 接口文档一致）
     * @param apiToken    可选鉴权 Token，若未启用鉴权可传 null 或空字符串
     */
    public static void broadcastPolicy(String headlessDns, int port, String policyJson, String apiToken) throws Exception {
        // 1) 解析 headless Service 域名，获取所有 Pod IP（多条 A 记录）
        InetAddress[] addrs = InetAddress.getAllByName(headlessDns);
        // 2) 预先把 JSON 转成字节数组，后续每个请求复用
        byte[] body = policyJson.getBytes(StandardCharsets.UTF_8);

        for (InetAddress addr : addrs) {
            // 3) 拼接每个 Pod 的 /apply 接口地址
            String url = "http://" + addr.getHostAddress() + ":" + port + "/apply";
            // 4) 创建 HTTP 连接并配置请求方法、超时、请求头
            HttpURLConnection conn = (HttpURLConnection) new URL(url).openConnection();
            conn.setRequestMethod("POST");
            conn.setConnectTimeout(3000);
            conn.setReadTimeout(5000);
            conn.setDoOutput(true);
            conn.setRequestProperty("Content-Type", "application/json");
            if (apiToken != null && !apiToken.isBlank()) {
                conn.setRequestProperty("X-API-Token", apiToken);
            }

            // 5) 写入请求体（策略 JSON）并发送请求
            try (OutputStream os = conn.getOutputStream()) {
                os.write(body);
            }

            // 6) 检查响应码，非 200 直接抛错
            int code = conn.getResponseCode();
            if (code != 200) {
                throw new RuntimeException("apply failed: " + url + " code=" + code);
            }
            // 7) 释放连接资源
            conn.disconnect();
        }
    }
}
