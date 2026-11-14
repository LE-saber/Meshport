package io.meshport.app

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.util.Log
import android.webkit.MimeTypeMap
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import meshcore.Meshcore
import meshcore.Platform
import java.io.File
import java.util.concurrent.atomic.AtomicInteger

/**
 * Foreground service that hosts the Go core (presence, P2P, relay, frpc).
 * The activity only displays the core's local web UI.
 */
class MeshService : Service() {

    private val main = Handler(Looper.getMainLooper())
    private var wakeLock: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        val type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        ServiceCompat.startForeground(this, NOTIF_SERVICE, serviceNotification(getString(R.string.starting)), type)
        startCore()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_QUIT) {
            quit()
            return START_NOT_STICKY
        }
        if (url == null && !starting) startCore()
        return START_STICKY
    }

    private fun startCore() {
        starting = true
        Thread {
            try {
                val dl = File(getExternalFilesDir(null) ?: filesDir, "Meshport").apply { mkdirs() }
                val osv = "Android ${Build.VERSION.RELEASE} (${Build.MANUFACTURER} ${Build.MODEL})"
                val ver = packageManager.getPackageInfo(packageName, 0).versionName ?: "dev"
                val u = Meshcore.start(File(filesDir, "core").path, dl.path, osv, ver, platform)
                main.post {
                    url = u
                    starting = false
                    listeners.toList().forEach { it(u, null) }
                    updateNotification()
                }
            } catch (e: Exception) {
                Log.e(TAG, "core start failed", e)
                main.post {
                    starting = false
                    listeners.toList().forEach { it(null, e.message ?: e.toString()) }
                }
            }
        }.start()
        main.postDelayed(statusTicker, 5000)
    }

    private var lastStatus = ""
    private val statusTicker = object : Runnable {
        override fun run() {
            val s = Meshcore.status()
            if (s != lastStatus) {
                lastStatus = s
                updateNotification()
                // hold a partial wake lock only while connected, so the socket survives doze briefly
                if (s == "online") acquireWake() else releaseWake()
            }
            if (s != "stopped") main.postDelayed(this, 5000)
        }
    }

    private fun statusText(): String = when (Meshcore.status()) {
        "online" -> "已連線 · " + getString(R.string.service_text)
        "connecting" -> "連線中…"
        "offline" -> "離線，稍後自動重試"
        "pending" -> "等待管理員核准"
        "setup" -> "尚未設定，點此開啟"
        "disabled" -> "裝置已被停用"
        else -> getString(R.string.starting)
    }

    private fun updateNotification() {
        if (ContextCompat.checkSelfPermission(this, android.Manifest.permission.POST_NOTIFICATIONS) == android.content.pm.PackageManager.PERMISSION_GRANTED || Build.VERSION.SDK_INT < 33) {
            NotificationManagerCompat.from(this).notify(NOTIF_SERVICE, serviceNotification(statusText()))
        }
    }

    private fun serviceNotification(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val quit = PendingIntent.getService(this, 1, Intent(this, MeshService::class.java).setAction(ACTION_QUIT), PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Builder(this, MeshApp.CHANNEL_SERVICE)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(open)
            .addAction(0, getString(R.string.action_quit), quit)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()
    }

    private fun acquireWake() {
        if (wakeLock?.isHeld == true) return
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "meshport:core").apply {
            setReferenceCounted(false)
            acquire(10 * 60 * 1000L)
        }
    }

    private fun releaseWake() {
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
    }

    private fun quit() {
        main.removeCallbacks(statusTicker)
        Thread { Meshcore.stop() }.start()
        url = null
        releaseWake()
        listeners.toList().forEach { it(null, "quit") }
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        main.removeCallbacks(statusTicker)
        releaseWake()
        super.onDestroy()
    }

    /** Callbacks from the Go core. */
    private val platform = object : Platform {
        override fun show(title: String, body: String) {
            if (MainActivity.visible) return
            if (Build.VERSION.SDK_INT >= 33 &&
                ContextCompat.checkSelfPermission(this@MeshService, android.Manifest.permission.POST_NOTIFICATIONS) != android.content.pm.PackageManager.PERMISSION_GRANTED
            ) return
            val open = PendingIntent.getActivity(this@MeshService, 2, Intent(this@MeshService, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
            val n = NotificationCompat.Builder(this@MeshService, MeshApp.CHANNEL_MESSAGES)
                .setSmallIcon(R.drawable.ic_stat)
                .setContentTitle(title)
                .setContentText(body)
                .setStyle(NotificationCompat.BigTextStyle().bigText(body))
                .setAutoCancel(true)
                .setContentIntent(open)
                .setPriority(NotificationCompat.PRIORITY_HIGH)
                .build()
            NotificationManagerCompat.from(this@MeshService).notify(nextId.incrementAndGet(), n)
        }

        override fun setAutostart(on: Boolean) {
            getSharedPreferences(MeshApp.PREFS, MODE_PRIVATE).edit().putBoolean(MeshApp.PREF_AUTOSTART, on).apply()
        }

        override fun openFile(path: String) {
            main.post {
                try {
                    val f = File(path)
                    val uri: Uri = FileProvider.getUriForFile(this@MeshService, "$packageName.files", f)
                    val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(f.extension.lowercase()) ?: "*/*"
                    val i = Intent(Intent.ACTION_VIEW).setDataAndType(uri, mime)
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
                    startActivity(Intent.createChooser(i, f.name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                } catch (e: Exception) {
                    Log.w(TAG, "open file failed", e)
                }
            }
        }
    }

    companion object {
        private const val TAG = "MeshService"
        private const val NOTIF_SERVICE = 1
        const val ACTION_QUIT = "io.meshport.app.QUIT"
        private val nextId = AtomicInteger(100)

        @Volatile var url: String? = null
        @Volatile private var starting = false

        /** (url, error) — called on the main thread. */
        val listeners = mutableListOf<(String?, String?) -> Unit>()

        fun start(ctx: Context) {
            ContextCompat.startForegroundService(ctx, Intent(ctx, MeshService::class.java))
        }
    }
}
