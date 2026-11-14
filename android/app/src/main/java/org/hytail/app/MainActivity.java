package org.hytail.app;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.*;
import android.content.ClipboardManager;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.*;
import android.text.*;
import android.text.method.*;
import android.text.style.*;
import android.view.*;
import android.widget.*;
import org.json.JSONObject;

/** Minimal mobile UI. Android components follow the platform lifecycle. */
public final class MainActivity extends Activity {
    private static final int BG = 0xff1e1e2e, PANEL = 0xff313244, FG = 0xffcdd6f4;
    private static final int SECONDARY = 0xffa6adc8, SAPPHIRE = 0xff74c7ec;
    private Spinner preset, protocol;
    private EditText name, port, target, original, bridgeCode, localPort;
    private CheckBox lan;
    private TextView output, status, health;
    private View hostFields, joinFields, hytaleField, protocolField, portField;
    private final Handler handler = new Handler(Looper.getMainLooper());
    private String last = "";
    private boolean showHost = true, presetChanging = false;
    private final Runnable classifyPastedCode = () -> detectBridgeCode();
    private final Runnable update = new Runnable() {
        @Override public void run() { updateState(); handler.postDelayed(this, 1100); }
    };
    private int dp(int d) { return Math.round(d * getResources().getDisplayMetrics().density); }
    private LinearLayout vertical() { LinearLayout x=new LinearLayout(this);x.setOrientation(LinearLayout.VERTICAL);return x; }
    private LinearLayout.LayoutParams lp(int height) { return new LinearLayout.LayoutParams(-1, height < 0 ? height : dp(height)); }
    private void addSpace(LinearLayout to, int size) { View v=new View(this);to.addView(v,lp(size)); }
    private TextView text(String value, int size, int color) {
        TextView v=new TextView(this);v.setText(value);v.setTextSize(size);v.setTextColor(color);
        v.setGravity(Gravity.CENTER_VERTICAL);return v;
    }
    private EditText field(String hint, boolean multiline) {
        EditText v=new EditText(this);v.setSingleLine(!multiline);v.setHint(hint);
        v.setTextSize(16);v.setTextColor(FG);v.setHintTextColor(SECONDARY);
        v.setBackgroundTintList(android.content.res.ColorStateList.valueOf(SAPPHIRE));
        v.setInputType(multiline ? android.text.InputType.TYPE_CLASS_TEXT | android.text.InputType.TYPE_TEXT_FLAG_MULTI_LINE
                : android.text.InputType.TYPE_CLASS_TEXT);
        if (multiline) {v.setMinLines(2);v.setMaxLines(5);v.setGravity(Gravity.TOP);}
        return v;
    }
    private Button button(String value) {
        Button b=new Button(this);b.setText(value);b.setAllCaps(false);b.setTextSize(15);
        b.setTextColor(BG);b.setBackgroundTintList(android.content.res.ColorStateList.valueOf(SAPPHIRE));return b;
    }
    private Spinner spinner(String[] entries) {
        Spinner s=new Spinner(this);
        ArrayAdapter<String> a=new ArrayAdapter<>(this, android.R.layout.simple_spinner_dropdown_item, entries);
        s.setAdapter(a);s.setBackgroundTintList(android.content.res.ColorStateList.valueOf(SAPPHIRE));
        return s;
    }
    private String value(EditText x) { return x.getText().toString().trim(); }
    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);getWindow().setStatusBarColor(0xff11111b);
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS},2);
        ScrollView scroll=new ScrollView(this);scroll.setFillViewport(true);scroll.setBackgroundColor(BG);
        LinearLayout root=vertical();root.setPadding(dp(18),dp(20),dp(18),dp(18));
        scroll.addView(root);setContentView(scroll);
        TextView title=text("HyTail",26,SAPPHIRE);title.setTypeface(null,1);root.addView(title);
        root.addView(text("Private TCP/UDP bridges over Tailcat",13,SECONDARY));addSpace(root,18);
        TextView t=text("Preset",14,SECONDARY);root.addView(t);
        preset=spinner(new String[]{"Generic","Hytale","Minecraft Java","Minecraft Bedrock","Terraria"});root.addView(preset);
        addSpace(root,10);
        LinearLayout tabs=new LinearLayout(this);tabs.setOrientation(LinearLayout.HORIZONTAL);
        Button host=button("Host");Button join=button("Join");
        tabs.addView(host,new LinearLayout.LayoutParams(0,dp(48),1));tabs.addView(join,new LinearLayout.LayoutParams(0,dp(48),1));
        host.setOnClickListener(v->setRole(true));join.setOnClickListener(v->setRole(false));root.addView(tabs);addSpace(root,12);
        LinearLayout hf=vertical();root.addView(hf);hostFields=hf;
        name=field("Service name",false);hf.addView(text("Name",14,SECONDARY));hf.addView(name);name.setText("My bridge");
        LinearLayout pf=vertical();hf.addView(pf);protocolField=pf;
        pf.addView(text("Protocol",14,SECONDARY));protocol=spinner(new String[]{"TCP","UDP","TCP + UDP"});pf.addView(protocol);
        LinearLayout po=vertical();hf.addView(po);portField=po;
        po.addView(text("Service ports (e.g. 25566, 25567-25568)",14,SECONDARY));port=field("8080, 8081-8083",false);port.setInputType(android.text.InputType.TYPE_CLASS_TEXT);po.addView(port);port.setText("8080");
        hf.addView(text("Host target (loopback or assigned private IP)",14,SECONDARY));target=field("127.0.0.1",false);hf.addView(target);target.setText("127.0.0.1");
        LinearLayout hyt=vertical();hf.addView(hyt);hytaleField=hyt;
        hyt.addView(text("Original Hytale multiplayer code",14,SECONDARY));original=field("Paste code copied in Hytale",true);hyt.addView(original);
        addSpace(hf,12);
        Button startHost=button("Start hosting");hf.addView(startHost);
        startHost.setOnClickListener(v->startHost());
        LinearLayout jf=vertical();root.addView(jf);joinFields=jf;
        jf.addView(text("Bridge invitation code",14,SECONDARY));
        bridgeCode=field("Paste invitation code",true);jf.addView(bridgeCode);
        bridgeCode.addTextChangedListener(new SimpleWatcher() {
            @Override public void afterTextChanged(Editable s) {
                handler.removeCallbacks(classifyPastedCode);
                handler.postDelayed(classifyPastedCode, 150);
            }
        });
        original.addTextChangedListener(new SimpleWatcher() {
            @Override public void afterTextChanged(Editable s) {
                if (!showHost || !isBridgeCode(s.toString())) return;
                String pasted = s.toString().trim();
                if (pasted.length() > 65536) return;
                try {
                    // Never navigate away on a partial or invalid paste.
                    String detected = Mobile.invoke("DetectInvitePreset", pasted);
                    if (detected.isEmpty()) return;
                    bridgeCode.setText(pasted);
                    setRole(false);
                    selectJoinPreset(detected);
                } catch (Exception ignored) { /* Not a complete bridge invitation. */ }
            }
        });
        Button paste=button("Paste from clipboard");jf.addView(paste);
        paste.setOnClickListener(v->{ClipboardManager c=(ClipboardManager)getSystemService(CLIPBOARD_SERVICE);
            ClipData d=c.getPrimaryClip();if(d!=null && d.getItemCount()>0)bridgeCode.setText(d.getItemAt(0).coerceToText(this));});
        jf.addView(text("Local ports (optional: single base or same-size list)",14,SECONDARY));
        localPort=field("Automatic or 25566-25568",false);localPort.setInputType(android.text.InputType.TYPE_CLASS_TEXT);
        jf.addView(localPort);
        lan=new CheckBox(this);lan.setText("Expose local join port to nearby LAN devices");
        lan.setTextColor(FG);jf.addView(lan);
        jf.addView(text("Off by default. Turn on only when another device needs this phone as a bridge.",12,SECONDARY));
        addSpace(jf,12);Button startJoin=button("Connect");jf.addView(startJoin);
        startJoin.setOnClickListener(v->startJoin());
        addSpace(root,14);
        status=text("Status: stopped",15,SECONDARY);root.addView(status);
        health=text("",12,SECONDARY);root.addView(health);
        output=text("",14,FG);output.setTextIsSelectable(true);output.setPadding(dp(12),dp(12),dp(12),dp(12));
        output.setBackgroundColor(PANEL);root.addView(output);
        LinearLayout commands=new LinearLayout(this);commands.setOrientation(LinearLayout.HORIZONTAL);
        Button copy=button("Copy result");Button diag=button("Diagnostics");Button stop=button("Stop");
        commands.addView(copy,new LinearLayout.LayoutParams(0,dp(48),1));
        commands.addView(diag,new LinearLayout.LayoutParams(0,dp(48),1));
        commands.addView(stop,new LinearLayout.LayoutParams(0,dp(48),1));
        root.addView(commands);
        copy.setOnClickListener(v->{String val=output.getText().toString();if(val.isEmpty())return;
            ((ClipboardManager)getSystemService(CLIPBOARD_SERVICE)).setPrimaryClip(ClipData.newPlainText("HyTail invite",val));
            Toast.makeText(this,"Copied",Toast.LENGTH_SHORT).show();});
        diag.setOnClickListener(v->showDiagnostics());
        stop.setOnClickListener(v->startService(new Intent(this,BridgeService.class).setAction(BridgeService.STOP)));
        addSpace(root,18);
        LinearLayout footerRow=new LinearLayout(this);footerRow.setGravity(Gravity.CENTER_VERTICAL);
        TextView footer=text("MIT licensed · Built upon Tailcat · Tailscale",12,SECONDARY);
        String foot=footer.getText().toString();SpannableString links=new SpannableString(foot);
        addLink(links,foot,"Tailcat","https://github.com/tailscale/tailcat");
        addLink(links,foot,"Tailscale","https://tailscale.com");
        footer.setText(links);footer.setMovementMethod(LinkMovementMethod.getInstance());
        footerRow.addView(footer,new LinearLayout.LayoutParams(0,dp(42),1));
        TextView copyright=text("©",19,SAPPHIRE);copyright.setGravity(Gravity.CENTER);
        copyright.setContentDescription("About HyTail and licenses");
        copyright.setOnClickListener(v->showAbout());
        footerRow.addView(copyright,new LinearLayout.LayoutParams(dp(48),dp(48)));
        root.addView(footerRow);
        preset.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener(){
            @Override public void onItemSelected(AdapterView<?> parent, View view, int position,long id){presetChanged(position);}
            @Override public void onNothingSelected(AdapterView<?> parent){}
        });
        setRole(true);
    }
    private static boolean isBridgeCode(String code) {
        code = code.trim();
        return code.startsWith("TCBR1:") || code.startsWith("HTBR1:");
    }
    private void selectJoinPreset(String detected) {
        int index;
        switch (detected) {
            case "hytale": index = 1; break;
            case "minecraft-bedrock": index = 3; break;
            case "minecraft":
            case "minecraft-java": index = 2; break; // Mode is informative; transport comes from the invitation.
            case "terraria": index = 4; break;
            default: index = 0; break;
        }
        if (preset.getSelectedItemPosition() != index) preset.setSelection(index);
    }
    private void detectBridgeCode() {
        String code = value(bridgeCode);
        if (!isBridgeCode(code) || code.length() > 65536) return;
        try {
            String detected = Mobile.invoke("DetectInvitePreset", code);
            if (!detected.isEmpty()) selectJoinPreset(detected);
        } catch (Exception ignored) { /* The service reports invalid invitations on Connect. */ }
    }
    private static class SimpleWatcher implements TextWatcher {
        @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
        @Override public void onTextChanged(CharSequence s, int start, int before, int count) {}
        @Override public void afterTextChanged(Editable s) {}
    }
    private void addLink(SpannableString s,String whole,String target,String url){
        int p=whole.indexOf(target);if(p<0)return;
        s.setSpan(new ClickableSpan(){@Override public void onClick(View v){startActivity(new Intent(Intent.ACTION_VIEW,Uri.parse(url)));}
            @Override public void updateDrawState(TextPaint ds){ds.setColor(SAPPHIRE);ds.setUnderlineText(true);}},p,p+target.length(),0);
    }
    private void presetChanged(int index){
        hytaleField.setVisibility(index==1?View.VISIBLE:View.GONE);
        protocolField.setVisibility(index==0?View.VISIBLE:View.GONE);
        portField.setVisibility(index==1?View.GONE:View.VISIBLE);
        if (!showHost) return; // Selecting a Join preset must not rewrite Host settings.
        if(index==1){name.setText("Hytale");}
        if(index==2){port.setText("25565");name.setText("Minecraft Java");protocol.setSelection(0);}
        if(index==3){
            port.setText("19132");name.setText("Minecraft Bedrock");protocol.setSelection(2);
            Toast.makeText(this,"NetherNet may require more UDP ports than this preset forwards",Toast.LENGTH_LONG).show();
        }
        if(index==4){port.setText("7777");name.setText("Terraria");protocol.setSelection(0);}
        if(index==0){port.setText("8080");name.setText("My bridge");}
    }
    private void setRole(boolean host){showHost=host;hostFields.setVisibility(host?View.VISIBLE:View.GONE);
        joinFields.setVisibility(host?View.GONE:View.VISIBLE);
        if (!host) detectBridgeCode();
    }
    private void startHost(){
        int p=preset.getSelectedItemPosition();String ps=p==1?"hytale":p==4?"terraria":p>=2?"minecraft":"generic";
        String pr=p==1?"udp":p==3?"both":(p==2||p==4)?"tcp":new String[]{"tcp","udp","both"}[protocol.getSelectedItemPosition()];
        if(p!=1 && value(port).isEmpty()){Toast.makeText(this,"Enter a port",Toast.LENGTH_SHORT).show();return;}
        Intent i=new Intent(this,BridgeService.class).setAction(BridgeService.START_HOST)
                .putExtra("preset",ps).putExtra("protocol",pr).putExtra("port",p==1?"1":value(port))
                .putExtra("target",value(target)).putExtra("name",value(name))
                .putExtra("original",value(original));
        getApplicationContext().startForegroundService(i);
    }
    private void startJoin(){
        if(value(bridgeCode).isEmpty()){Toast.makeText(this,"Paste an invite",Toast.LENGTH_SHORT).show();return;}
        Intent i=new Intent(this,BridgeService.class).setAction(BridgeService.START_JOIN)
                .putExtra("code",value(bridgeCode)).putExtra("bind",lan.isChecked()?"lan":"loopback")
                .putExtra("localPort",value(localPort));
        getApplicationContext().startForegroundService(i);
    }
    private void updateState(){
        try {
            JSONObject o=new JSONObject(BridgeService.snapshot());String state=o.optString("status","stopped");
            String phase=o.optString("phase",state);
            String phaseLine="Status: "+state+" / "+phase;
            if ("reconnecting".equals(phase)) phaseLine += " (retry "+o.optInt("retryAttempt")+")";
            else if ("online".equals(phase)) phaseLine += " / "+o.optString("path","unknown");
            status.setText(phaseLine);
            String diagnostic="Service: "+o.optString("serviceStatus","unverified");
            if (!o.optString("localPortPolicy").isEmpty()) diagnostic += " | Ports: "+o.optString("localPortPolicy");
            if (!o.optString("lastError").isEmpty()) diagnostic += "\n"+o.optString("lastError");
            health.setText(diagnostic);
            // For advanced DNS invitations, the actual type is known only after
            // resolution in the networking service; update the selected tab then.
            if ("running".equals(state) && "join".equals(o.optString("role"))) {
                String resolvedPreset = o.optString("preset", "generic");
                if ("minecraft".equals(resolvedPreset)) {
                    resolvedPreset = "tcp".equals(o.optString("protocol")) ? "minecraft-java" : "minecraft-bedrock";
                }
                selectJoinPreset(resolvedPreset);
            }
            if(!state.equals("stopped")){String data=o.optString("invite",o.optString("result",""));
                if(data.isEmpty() && "error".equals(state))data=o.optString("error","");
                if(!data.isEmpty() && !data.equals(last)){output.setText(data);last=data;}
            }
        }catch(Exception ignored){}
    }
    private String readAsset(String path) {
        try (java.io.InputStream input=getAssets().open(path)) {
            java.io.ByteArrayOutputStream buffer=new java.io.ByteArrayOutputStream();
            byte[] part=new byte[4096];int length;
            while ((length=input.read(part))!=-1) buffer.write(part,0,length);
            return buffer.toString("UTF-8");
        } catch (java.io.IOException e) {
            return "License information unavailable: "+e.getMessage();
        }
    }
    private void showAbout() {
        ScrollView scroll=new ScrollView(this);scroll.setBackgroundColor(BG);
        LinearLayout content=vertical();content.setPadding(dp(20),dp(16),dp(20),dp(20));
        scroll.addView(content);
        content.addView(text("By Lucas \"fptbb\" Oliveira and contributors",16,FG));
        content.addView(text("Copyright © 2026 Lucas \"fptbb\" Oliveira and HyTail contributors",13,SECONDARY));
        content.addView(text("HyTail license: MIT",13,SAPPHIRE));
        addSpace(content,14);
        content.addView(text("Direct dependencies",18,SAPPHIRE));
        String inventory=readAsset("DEPENDENCIES.tsv");
        for (String record:inventory.split("\n")) {
            if (record.trim().isEmpty() || record.startsWith("#")) continue;
            String[] cols=record.split("\t",3);
            if (cols.length!=3) continue;
            String line=cols[0]+" — "+cols[1];
            TextView entry=text(line,14,FG);
            SpannableString link=new SpannableString(line);
            addLink(link,line,cols[0],cols[2]);
            entry.setText(link);entry.setMovementMethod(LinkMovementMethod.getInstance());
            content.addView(entry);
        }
        addSpace(content,12);
        content.addView(text("Direct dependencies only. Consult THIRD_PARTY_LICENSES.md before distributing binaries.",12,SECONDARY));
        addSpace(content,10);
        TextView legal=text(readAsset("LICENSE_TEXTS.txt"),12,SECONDARY);
        legal.setTextIsSelectable(true);content.addView(legal);
        new AlertDialog.Builder(this).setTitle("About HyTail · Licenses")
                .setView(scroll).setPositiveButton("Close",(d,w)->{}).show();
    }
    private void showDiagnostics(){
        try{JSONObject o=new JSONObject(BridgeService.snapshot());
            new AlertDialog.Builder(this).setTitle("Tailcat diagnostics").setMessage(o.toString(2))
                    .setPositiveButton("Close",(d,w)->{}).show();
        }catch(Exception e){Toast.makeText(this,e.toString(),Toast.LENGTH_LONG).show();}
    }
    @Override protected void onResume(){super.onResume();handler.post(update);}
    @Override protected void onPause(){handler.removeCallbacks(update);super.onPause();}
}
