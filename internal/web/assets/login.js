"use strict";
document.getElementById("login-form").addEventListener("submit",async e=>{
 e.preventDefault();const button=document.getElementById("login-submit");const input=document.getElementById("access-key");const error=document.getElementById("login-error");button.disabled=true;error.hidden=true;
 try{
  const response=await fetch("/api/auth/login",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({token:input.value})});
  const data=await response.json();if(!response.ok)throw new Error(data.message);
  input.value="";location.assign("/");
 }catch(e){error.textContent=e.message||"Не удалось войти.";error.hidden=false;}
 finally{button.disabled=false;}
});
