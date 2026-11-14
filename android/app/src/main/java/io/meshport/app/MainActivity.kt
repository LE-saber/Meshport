package io.meshport.app

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.ContentValues
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.os.PowerManager
import android.provider.MediaStore
import android.provider.Settings
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL

class MainActivity : ComponentActivity() {

    private lateinit var web: WebView
    private lateinit var progress: ProgressBar
    private lateinit var message: TextView
    private var fileCallback: ValueCallback<Array<Uri>>? = null

    private val pickFiles = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { res ->
        fileCallback?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(res.resultCode, res.data))
        fileCallback = null
    }
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) { }

    private val onCore: (String?, String?) -> Unit = { url, err ->
        when {
            url != null -> load(url)
            err == "quit" -> finish()
            err != null -> showMessage(getString(R.string.start_failed, err))
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = FrameLayout(this)
        web = WebView(this)
        progress = ProgressBar(this)
        message = TextView(this).apply { setPadding(48, 48, 48, 48); visibility = View.GONE }
        root.addView(web, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        root.addView(progress, FrameLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT, android.view.Gravity.CENTER))
        root.addView(message, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, android.view.Gravity.CENTER))
        setContentView(root)
        // edge-to-edge (forced on Android 15): keep content clear of the system bars and the keyboard
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }

        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = false
            setSupportMultipleWindows(false)
        }
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, req: WebResourceRequest): Boolean {
                val u = req.url
                if (u.host == "127.0.0.1" || u.host == "localhost") return false
                try {
                    startActivity(Intent(Intent.ACTION_VIEW, u))
                } catch (_: ActivityNotFoundException) { }
                return true
            }

            override fun onPageFinished(view: WebView, url: String) {
                progress.visibility = View.GONE
            }
        }
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, cb: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                fileCallback?.onReceiveValue(null)
                fileCallback = cb
                return try {
                    val i = params.createIntent().putExtra(Intent.EXTRA_ALLOW_MULTIPLE, params.mode == FileChooserParams.MODE_OPEN_MULTIPLE)
                    pickFiles.launch(i)
                    true
                } catch (e: ActivityNotFoundException) {
                    fileCallback = null
                    false
                }
            }
        }
        web.setDownloadListener { url, _, contentDisposition, mimetype, _ ->
            saveDownload(url, contentDisposition, mimetype)
        }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (web.canGoBack()) web.goBack() else moveTaskToBack(true)
            }
        })

        if (Build.VERSION.SDK_INT >= 33) askNotify.launch(android.Manifest.permission.POST_NOTIFICATIONS)
        askBatteryOnce()

        MeshService.listeners.add(onCore)
        val existing = MeshService.url
        if (existing != null) load(existing) else MeshService.start(this)
    }

    override fun onDestroy() {
        MeshService.listeners.remove(onCore)
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        visible = true
    }

    override fun onPause() {
        visible = false
        super.onPause()
    }

    private fun load(url: String) {
        message.visibility = View.GONE
        if (web.url == null || !web.url!!.startsWith(url.substringBefore("/auth"))) web.loadUrl(url)
    }

    private fun showMessage(text: String) {
        progress.visibility = View.GONE
        message.text = text
        message.visibility = View.VISIBLE
    }

    /** Ask once to be exempt from battery optimizations so the connection stays up. */
    @SuppressLint("BatteryLife")
    private fun askBatteryOnce() {
        val prefs = getSharedPreferences(MeshApp.PREFS, MODE_PRIVATE)
        if (prefs.getBoolean("asked_battery", false)) return
        prefs.edit().putBoolean("asked_battery", true).apply()
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        if (!pm.isIgnoringBatteryOptimizations(packageName)) {
            try {
                startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
            } catch (_: Exception) { }
        }
    }

    /** Copies a file served by the local core into the public Downloads folder. */
    private fun saveDownload(url: String, contentDisposition: String?, mimetype: String?) {
        val cookie = CookieManager.getInstance().getCookie(url)
        val name = URLUtil.guessFileName(url, contentDisposition, mimetype)
        Thread {
            try {
                val conn = URL(url).openConnection() as HttpURLConnection
                if (cookie != null) conn.setRequestProperty("Cookie", cookie)
                conn.inputStream.use { input ->
                    if (Build.VERSION.SDK_INT >= 29) {
                        val values = ContentValues().apply {
                            put(MediaStore.Downloads.DISPLAY_NAME, name)
                            put(MediaStore.Downloads.MIME_TYPE, mimetype ?: "application/octet-stream")
                            put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/Meshport")
                        }
                        val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                            ?: throw IllegalStateException("MediaStore insert failed")
                        contentResolver.openOutputStream(uri)!!.use { input.copyTo(it) }
                    } else {
                        val dir = File(getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), "Meshport").apply { mkdirs() }
                        FileOutputStream(File(dir, name)).use { input.copyTo(it) }
                    }
                }
                runOnUiThread { Toast.makeText(this, getString(R.string.saved_to, name), Toast.LENGTH_LONG).show() }
            } catch (e: Exception) {
                runOnUiThread { Toast.makeText(this, getString(R.string.save_failed, e.message ?: e.toString()), Toast.LENGTH_LONG).show() }
            }
        }.start()
    }

    companion object {
        @Volatile var visible = false
    }
}
