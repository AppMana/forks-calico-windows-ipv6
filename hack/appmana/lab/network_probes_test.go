package main

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func networkProbeObjects(winImage string) []runtime.Object {
	psArgs := func(script string) []string {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; " + script}
	}
	var objects []runtime.Object
	for _, name := range []string{"linux", "windows"} {
		container := v1.Container{Name: "server", Image: "docker.io/nicolaka/netshoot@sha256:34eeca872db74067b1ed7fdc6201f278578bf57df7bd3081e99b5097a28464b5", ImagePullPolicy: v1.PullNever, Command: []string{"sh", "-ec", `mkdir -p /tmp/www; printf 'ok linux' > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www`}}
		if name == "windows" {
			container.Image = winImage
			container.Command = psArgs(`$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Any,8080); $l.Start(); while($true){$c=$l.AcceptTcpClient();try{$s=$c.GetStream();$s.ReadTimeout=5000;$b=New-Object byte[] 4096;$null=$s.Read($b,0,$b.Length);$r=[Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK` + "`r`nContent-Length: 10`r`nConnection: close`r`n`r`nok windows" + `");$s.Write($r,0,$r.Length)}catch{}finally{$c.Dispose()}}`)
		}
		objects = append(objects, &v1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "hc-" + name, Namespace: "default", Labels: map[string]string{"app": "hc-" + name}}, Spec: v1.PodSpec{NodeName: name, Containers: []v1.Container{container}, RestartPolicy: v1.RestartPolicyAlways}},
			&v1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "svc-hc-" + name + "-v4", Namespace: "default"}, Spec: v1.ServiceSpec{Selector: map[string]string{"app": "hc-" + name}, Ports: []v1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}})
	}
	return objects
}
