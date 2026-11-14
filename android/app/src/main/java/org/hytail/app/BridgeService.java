package org.hytail.app;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.net.ConnectivityManager;
import android.net.Network;
import android.os.*;
import org.json.JSONObject;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Foreground service: bridge remains active when Activity is dismissed. */
public final class BridgeService extends Service {
    static final String START_HOST = "org.hytail.START_HOST";
    static final String START_JOIN = "org.hytail.START_JOIN";
    static final String STOP = "org.hytail.STOP";
    private static final String CHANNEL = "hytail_bridge";
    private static final int NOTIFICATION_ID = 103;
    private final ExecutorService worker = Executors.newCachedThreadPool();
    private final Handler handler = new Handler(Looper.getMainLooper());
    private volatile String latest = "{\"status\":\"stopped\"}";
    private static volatile String sharedState = "{\"status\":\"stopped\"}";
    private void publish(String value) { latest = value; sharedState = value; }
    private volatile boolean foreground = false;
    private PowerManager.WakeLock lock;
    private ConnectivityManager connectivity;
    private ConnectivityManager.NetworkCallback networkCallback;
    private volatile String lastNotificationText = "";
    private final Runnable pulse = new Runnable() {
        @Override public void run() {
            if (foreground) {
                worker.execute(() -> {
                    try {
                        publish(Mobile.invoke("Status"));
                        JSONObject data = new JSONObject(latest);
                        String phase = data.optString("phase", "starting");
                        String label;
                        if ("reconnecting".equals(phase)) {
                            label = "Reconnecting (retry " + data.optInt("retryAttempt") + ")";
                        } else if ("online".equals(phase)) {
                            label = "Connected - " + data.optString("path", "checking path");
                        } else if ("hosting".equals(phase)) {
                            label = "Hosting bridge";
                        } else {
                            label = "Connecting...";
                        }
                        if (!label.equals(lastNotificationText)) {
                            lastNotificationText = label;
                            getSystemService(NotificationManager.class)
                                .notify(NOTIFICATION_ID, makeNotification(label));
                        }
                    } catch (Exception ignored) {}
                });
                handler.postDelayed(this, 1800);
            }
        }
    };
    // Same-process snapshot only. Never holds the actual Tailcat secret in OS notifications.
    private static volatile BridgeService instance;
    static String snapshot() {
        BridgeService s = instance;
        return s == null ? sharedState : s.latest;
    }
    @Override public void onCreate() {
        super.onCreate(); instance = this;
        NotificationChannel channel = new NotificationChannel(CHANNEL, "HyTail bridge",
                NotificationManager.IMPORTANCE_LOW);
        getSystemService(NotificationManager.class).createNotificationChannel(channel);
        connectivity = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
        networkCallback = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network network) { signalNetworkChanged(); }
            @Override public void onLost(Network network) { signalNetworkChanged(); }
        };
        try { connectivity.registerDefaultNetworkCallback(networkCallback); }
        catch (Exception ignored) { networkCallback = null; }
    }
    private void signalNetworkChanged() {
        try { Mobile.invoke("NetworkChanged"); } catch (Exception ignored) { }
    }
    private Notification makeNotification(String text) {
        Intent open = new Intent(this, MainActivity.class).setFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP);
        PendingIntent openPi = PendingIntent.getActivity(this, 0, open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Intent stopIntent = new Intent(this, BridgeService.class).setAction(STOP);
        PendingIntent stopPi = PendingIntent.getService(this, 1, stopIntent,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this, CHANNEL)
                .setSmallIcon(android.R.drawable.stat_sys_upload)
                .setContentTitle("HyTail bridge")
                .setContentText(text)
                .setContentIntent(openPi)
                .setOngoing(true)
                .addAction(new Notification.Action.Builder(
                        android.R.drawable.ic_media_pause, "Stop bridge", stopPi).build())
                .build();
    }
    private void foreground(String reason) {
        if (!foreground) {
            if (Build.VERSION.SDK_INT >= 29) {
                startForeground(NOTIFICATION_ID, makeNotification(reason),
                        ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
            } else startForeground(NOTIFICATION_ID, makeNotification(reason));
            foreground = true;
            PowerManager power = (PowerManager) getSystemService(POWER_SERVICE);
            lock = power.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "HyTail:bridge");
            lock.acquire();
            handler.post(pulse);
        }
    }
    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent == null) return START_NOT_STICKY;
        String action = intent.getAction();
        if (STOP.equals(action)) { worker.execute(this::stopWorker); return START_NOT_STICKY; }
        if (!START_HOST.equals(action) && !START_JOIN.equals(action)) return START_NOT_STICKY;
        if (foreground) return START_NOT_STICKY; // Ignore duplicate Start taps.
        // The user must explicitly start the bridge while the Activity is visible.
        foreground("Connecting...");
        publish("{\"status\":\"starting\"}");
        worker.execute(() -> {
            try {
                if (START_HOST.equals(action)) {
                    publish(Mobile.invoke("StartHost", intent.getStringExtra("preset"),
                            intent.getStringExtra("protocol"), intent.getStringExtra("port"),
                            intent.getStringExtra("target"), intent.getStringExtra("name"),
                            intent.getStringExtra("original")));
                } else {
                    publish(Mobile.invoke("StartJoin", intent.getStringExtra("code"),
                            intent.getStringExtra("bind"), intent.getStringExtra("localPort")));
                }
                JSONObject status = new JSONObject(latest);
                if (!"running".equals(status.optString("status"))) {
                    shutdownForeground();
                } else {
                    getSystemService(NotificationManager.class).notify(NOTIFICATION_ID,
                            makeNotification(START_HOST.equals(action) ? "Hosting bridge" : "Local ports reserved - connecting"));
                }
            } catch (Exception e) {
                publish("{\"status\":\"error\",\"error\":" + JSONObject.quote(e.toString()) + "}");
                shutdownForeground();
            }
        });
        return START_NOT_STICKY;
    }
    private void stopWorker() {
        try { Mobile.invoke("Stop"); } catch (Exception ignored) {}
        publish("{\"status\":\"stopped\"}");
        shutdownForeground();
    }
    private void shutdownForeground() {
        handler.post(() -> {
            foreground = false;
            handler.removeCallbacks(pulse);
            if (lock != null && lock.isHeld()) lock.release();
            stopForeground(STOP_FOREGROUND_REMOVE);
            stopSelf();
        });
    }
    @Override public void onDestroy() {
        foreground = false;
        handler.removeCallbacks(pulse);
        if (lock != null && lock.isHeld()) lock.release();
        if (instance == this) instance = null;
        if (connectivity != null && networkCallback != null) {
            try { connectivity.unregisterNetworkCallback(networkCallback); }
            catch (Exception ignored) { }
        }
        // Ensures sockets close when the system terminates the service.
        try { Mobile.invoke("Stop"); } catch (Exception ignored) {}
        worker.shutdownNow();
        super.onDestroy();
    }
    @Override public IBinder onBind(Intent intent) { return null; }
}
